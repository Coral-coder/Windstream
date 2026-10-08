package stream

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"golang.org/x/time/rate"

	"github.com/coral-coder/windstream/internal/input"
	"github.com/coral-coder/windstream/internal/protocol"
	"github.com/coral-coder/windstream/internal/safe"
)

// SignalMessage is the JSON envelope exchanged over the signaling WebSocket.
type SignalMessage struct {
	Type       string                   `json:"type"`
	SDP        string                   `json:"sdp,omitempty"`
	Candidate  *webrtc.ICECandidateInit `json:"candidate,omitempty"`
	Message    string                   `json:"message,omitempty"`
	ICEServers []webrtc.ICEServer       `json:"iceServers,omitempty"`
	Input      *InputCaps               `json:"input,omitempty"`
	// Codecs (hello, client -> server) lists the video codecs the browser
	// decodes, best first. Codec (config/offer, server -> client) names the
	// codec being streamed.
	Codecs []string `json:"codecs,omitempty"`
	Codec  string   `json:"codec,omitempty"`
}

// InputCaps tells the client which device classes the server accepts.
type InputCaps struct {
	Gamepads    bool `json:"gamepads"`
	Keyboard    bool `json:"keyboard"`
	Mouse       bool `json:"mouse"`
	MaxGamepads int  `json:"maxGamepads"`
}

// Client is one authenticated viewer/player.
type Client struct {
	ID   string
	User string

	hub  *Hub
	log  *slog.Logger
	send func(SignalMessage) error
	pc   *webrtc.PeerConnection
	inj  input.Injector

	// codecs and videoSender are guarded by hub.mu.
	codecs      []string
	videoSender *webrtc.RTPSender

	// sigMu serializes offer/answer/candidate handling: pion's
	// PeerConnection is not safe for concurrent description changes.
	sigMu sync.Mutex

	// Our ICE candidates are held back until the offer has been sent: a
	// browser drops candidates that arrive before the offer, and host
	// candidates are gathered almost instantly, so without this the
	// connection sometimes never completes.
	candMu    sync.Mutex
	offerSent bool
	pendCands []webrtc.ICECandidateInit

	limiter    *rate.Limiter
	dropped    atomic.Int64
	startOnce  sync.Once
	helloTimer atomic.Pointer[time.Timer]
	closeOnce  sync.Once
	done       chan struct{}
}

// helloTimeout is how long the server waits for the browser to list its
// codecs before assuming an old client that only handles H.264.
const helloTimeout = 3 * time.Second

// Connect creates a peer connection for an authenticated user and sends the
// session config through send. The browser answers with a "hello" listing
// the codecs it can decode, after which the server sends its offer. The
// caller must forward subsequent signaling messages to HandleSignal and call
// Close when the signaling channel ends.
func (h *Hub) Connect(user, ip string, send func(SignalMessage) error) (*Client, error) {
	idb := make([]byte, 6)
	_, _ = rand.Read(idb)
	c := &Client{
		ID:      hex.EncodeToString(idb),
		User:    user,
		hub:     h,
		send:    send,
		limiter: rate.NewLimiter(3000, 600),
		done:    make(chan struct{}),
	}
	c.log = h.log.With("client", c.ID, "user", user, "ip", ip)

	caps := InputCaps{}
	if h.cfg.InputEnabled {
		inj, err := input.New(h.cfg.Input, c.log)
		if inj == nil {
			inj = input.Noop{}
		}
		c.inj = inj
		switch {
		case err == nil:
			caps = InputCaps{Gamepads: h.cfg.Input.Gamepads, Keyboard: h.cfg.Input.Keyboard,
				Mouse: h.cfg.Input.Mouse, MaxGamepads: h.cfg.Input.MaxGamepads}
		case errors.Is(err, input.ErrUnsupported):
			c.log.Warn("input injection unavailable; stream is view-only", "error", err)
		default:
			// Only controllers are unavailable (e.g. the ViGEmBus driver is
			// missing): keyboard and mouse still work.
			c.log.Warn("controllers unavailable; keyboard and mouse still work", "error", err)
			caps = InputCaps{Keyboard: h.cfg.Input.Keyboard, Mouse: h.cfg.Input.Mouse}
		}
	} else {
		c.inj = input.Noop{}
	}

	// Build the peer connection before registering, so a concurrent
	// Hub.Close always finds a complete client to tear down.
	if err := c.setupPeer(); err != nil {
		if c.pc != nil {
			_ = c.pc.Close()
		}
		_ = c.inj.Close()
		return nil, err
	}
	if err := h.addClient(c); err != nil {
		_ = c.pc.Close()
		_ = c.inj.Close()
		return nil, err
	}
	if err := send(SignalMessage{Type: "config", ICEServers: h.iceServers, Input: &caps}); err != nil {
		c.Close()
		return nil, err
	}
	c.helloTimer.Store(time.AfterFunc(helloTimeout, func() {
		defer safe.Recover(c.log, "hello timeout")
		if err := c.start(nil); err != nil && !errors.Is(err, errStarted) {
			c.log.Warn("starting stream failed", "error", err)
			_ = c.send(SignalMessage{Type: "error", Message: startErrorMessage(err)})
			c.Close()
		}
	}))
	c.log.Info("client connected", "clients", h.ClientCount())
	return c, nil
}

var errStarted = errors.New("stream already started")

// start negotiates the video codec and sends the offer, once.
func (c *Client) start(codecs []string) error {
	err := errStarted
	c.startOnce.Do(func() {
		if t := c.helloTimer.Load(); t != nil {
			t.Stop()
		}
		if err = c.hub.attach(c, codecs); err != nil {
			return
		}
		err = c.sendOffer()
	})
	return err
}

func startErrorMessage(err error) string {
	if errors.Is(err, ErrNoCodec) {
		return "This browser cannot decode any video format this PC can encode. Use a current Chrome, Edge, Safari or Firefox."
	}
	return "Could not start the stream: " + err.Error()
}

func (c *Client) setupPeer() error {
	pc, err := c.hub.api.NewPeerConnection(webrtc.Configuration{ICEServers: c.hub.iceServers})
	if err != nil {
		return fmt.Errorf("new peer connection: %w", err)
	}
	c.pc = pc

	// The video track is added once the codec is known (see start).
	if c.hub.audio != nil {
		as, err := pc.AddTrack(c.hub.audio)
		if err != nil {
			return fmt.Errorf("add audio track: %w", err)
		}
		go drainRTCP(c.log, as)
	}

	ordered, unordered := true, false
	var zero uint16
	control, err := pc.CreateDataChannel("control", &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		return fmt.Errorf("create control channel: %w", err)
	}
	state, err := pc.CreateDataChannel("state", &webrtc.DataChannelInit{Ordered: &unordered, MaxRetransmits: &zero})
	if err != nil {
		return fmt.Errorf("create state channel: %w", err)
	}
	for _, dc := range []*webrtc.DataChannel{control, state} {
		dc := dc
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			defer safe.Recover(c.log, "input message")
			c.onInput(dc, msg.Data)
		})
	}

	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		defer safe.Recover(c.log, "ice candidate")
		if cand == nil {
			return
		}
		init := cand.ToJSON()
		c.candMu.Lock()
		defer c.candMu.Unlock()
		if !c.offerSent {
			c.pendCands = append(c.pendCands, init)
			return
		}
		if err := c.send(SignalMessage{Type: "candidate", Candidate: &init}); err != nil {
			c.log.Debug("send candidate", "error", err)
		}
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		defer safe.Recover(c.log, "connection state")
		c.log.Info("peer connection state", "state", s.String())
		switch s {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			c.Close()
		case webrtc.PeerConnectionStateDisconnected:
			time.AfterFunc(15*time.Second, func() {
				defer safe.Recover(c.log, "disconnect timer")
				if c.pc.ConnectionState() == webrtc.PeerConnectionStateDisconnected {
					c.log.Warn("peer stayed disconnected; closing")
					c.Close()
				}
			})
		}
	})
	return nil
}

func (c *Client) sendOffer() error {
	c.sigMu.Lock()
	defer c.sigMu.Unlock()
	offer, err := c.pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("create offer: %w", err)
	}
	if err := c.pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("set local description: %w", err)
	}
	if err := c.send(SignalMessage{Type: "offer", SDP: offer.SDP, Codec: c.Codec()}); err != nil {
		return err
	}
	c.candMu.Lock()
	defer c.candMu.Unlock()
	c.offerSent = true
	for i := range c.pendCands {
		if err := c.send(SignalMessage{Type: "candidate", Candidate: &c.pendCands[i]}); err != nil {
			c.log.Debug("send candidate", "error", err)
		}
	}
	c.pendCands = nil
	return nil
}

// HandleSignal processes a message from the client's signaling channel.
func (c *Client) HandleSignal(m SignalMessage) error {
	switch m.Type {
	case "answer":
		if len(m.SDP) > 64*1024 {
			return errors.New("answer too large")
		}
		c.sigMu.Lock()
		defer c.sigMu.Unlock()
		return c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: m.SDP})
	case "candidate":
		if m.Candidate == nil {
			return nil
		}
		c.sigMu.Lock()
		defer c.sigMu.Unlock()
		return c.pc.AddICECandidate(*m.Candidate)
	case "hello":
		if err := c.start(m.Codecs); err != nil && !errors.Is(err, errStarted) {
			return err
		}
		return nil
	case "bye":
		c.Close()
		return nil
	case "renegotiate":
		return c.sendOffer()
	default:
		return fmt.Errorf("unknown signal type %q", m.Type)
	}
}

// Codec returns the video codec this client is receiving.
func (c *Client) Codec() string {
	c.hub.mu.Lock()
	defer c.hub.mu.Unlock()
	return c.hub.codec
}

// Done is closed when the client has been torn down.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close tears down the peer connection and virtual input devices.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		if t := c.helloTimer.Load(); t != nil {
			t.Stop()
		}
		c.hub.removeClient(c)
		if c.pc != nil {
			_ = c.pc.Close()
		}
		if c.inj != nil {
			if err := c.inj.Close(); err != nil {
				c.log.Warn("closing input devices", "error", err)
			}
		}
		close(c.done)
		c.log.Info("client disconnected", "clients", c.hub.ClientCount(), "dropped_input", c.dropped.Load())
	})
}

func (c *Client) onInput(dc *webrtc.DataChannel, data []byte) {
	m, err := protocol.Decode(data)
	if err != nil {
		c.log.Debug("bad input message", "error", err, "len", len(data))
		return
	}
	// Only high-rate motion is rate limited (an 8 kHz gaming mouse can
	// exceed it): a dropped key or button release would leave it stuck.
	switch m.Type {
	case protocol.MsgMouseMove, protocol.MsgMouseWheel, protocol.MsgGamepadState:
		if !c.limiter.Allow() {
			if n := c.dropped.Add(1); n == 1 || n%1000 == 0 {
				c.log.Warn("input rate limit exceeded; dropping motion messages", "dropped", n)
			}
			return
		}
	}
	switch m.Type {
	case protocol.MsgPing:
		_ = dc.Send(protocol.EncodePong(m.Ping))
		return
	case protocol.MsgGamepadConnect:
		err = c.inj.GamepadConnect(int(m.Index), m.Name)
	case protocol.MsgGamepadDisconnect:
		err = c.inj.GamepadDisconnect(int(m.Index))
	case protocol.MsgGamepadState:
		err = c.inj.GamepadState(int(m.Index), m.Gamepad)
	case protocol.MsgKey:
		err = c.inj.Key(m.Key, m.Down)
	case protocol.MsgMouseMove:
		err = c.inj.MouseMove(int32(m.DX), int32(m.DY))
	case protocol.MsgMouseButton:
		err = c.inj.MouseButton(m.Button, m.Down)
	case protocol.MsgMouseWheel:
		err = c.inj.MouseWheel(int32(m.DX), int32(m.DY))
	}
	if err != nil {
		c.log.Warn("input injection failed", "type", fmt.Sprintf("%#x", m.Type), "error", err)
	}
}

// drainRTCP reads and discards RTCP from a sender so the interceptors keep
// running (NACK, receiver reports, TWCC feedback).
func drainRTCP(log *slog.Logger, s *webrtc.RTPSender) {
	defer safe.Recover(log, "rtcp reader")
	buf := make([]byte, 1500)
	for {
		if _, _, err := s.Read(buf); err != nil {
			return
		}
	}
}

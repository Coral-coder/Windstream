package stream

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"golang.org/x/time/rate"

	"github.com/coral-coder/windstream/internal/input"
	"github.com/coral-coder/windstream/internal/protocol"
)

// SignalMessage is the JSON envelope exchanged over the signaling WebSocket.
type SignalMessage struct {
	Type       string                   `json:"type"`
	SDP        string                   `json:"sdp,omitempty"`
	Candidate  *webrtc.ICECandidateInit `json:"candidate,omitempty"`
	Message    string                   `json:"message,omitempty"`
	ICEServers []webrtc.ICEServer       `json:"iceServers,omitempty"`
	Input      *InputCaps               `json:"input,omitempty"`
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

	limiter   *rate.Limiter
	dropped   int
	closeOnce sync.Once
	done      chan struct{}
}

// Connect creates a peer connection for an authenticated user, sends the
// initial offer through send and returns the client. The caller must forward
// subsequent signaling messages to HandleSignal and call Close when the
// signaling channel ends.
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
		if err != nil {
			c.log.Warn("input injection unavailable; stream is view-only", "error", err)
		}
		c.inj = inj
		if err == nil {
			caps = InputCaps{Gamepads: h.cfg.Input.Gamepads, Keyboard: h.cfg.Input.Keyboard,
				Mouse: h.cfg.Input.Mouse, MaxGamepads: h.cfg.Input.MaxGamepads}
		}
	} else {
		c.inj = input.Noop{}
	}

	if err := h.addClient(c); err != nil {
		_ = c.inj.Close()
		return nil, err
	}
	if err := c.setupPeer(); err != nil {
		c.Close()
		return nil, err
	}
	if err := send(SignalMessage{Type: "config", ICEServers: h.iceServers, Input: &caps}); err != nil {
		c.Close()
		return nil, err
	}
	if err := c.sendOffer(); err != nil {
		c.Close()
		return nil, err
	}
	c.log.Info("client connected", "clients", h.ClientCount())
	return c, nil
}

func (c *Client) setupPeer() error {
	pc, err := c.hub.api.NewPeerConnection(webrtc.Configuration{ICEServers: c.hub.iceServers})
	if err != nil {
		return fmt.Errorf("new peer connection: %w", err)
	}
	c.pc = pc

	vs, err := pc.AddTrack(c.hub.video)
	if err != nil {
		return fmt.Errorf("add video track: %w", err)
	}
	go drainRTCP(vs)
	if c.hub.audio != nil {
		as, err := pc.AddTrack(c.hub.audio)
		if err != nil {
			return fmt.Errorf("add audio track: %w", err)
		}
		go drainRTCP(as)
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
		dc.OnMessage(func(msg webrtc.DataChannelMessage) { c.onInput(dc, msg.Data) })
	}

	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand == nil {
			return
		}
		init := cand.ToJSON()
		if err := c.send(SignalMessage{Type: "candidate", Candidate: &init}); err != nil {
			c.log.Debug("send candidate", "error", err)
		}
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		c.log.Info("peer connection state", "state", s.String())
		switch s {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			c.Close()
		case webrtc.PeerConnectionStateDisconnected:
			time.AfterFunc(15*time.Second, func() {
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
	offer, err := c.pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("create offer: %w", err)
	}
	if err := c.pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("set local description: %w", err)
	}
	return c.send(SignalMessage{Type: "offer", SDP: offer.SDP})
}

// HandleSignal processes a message from the client's signaling channel.
func (c *Client) HandleSignal(m SignalMessage) error {
	switch m.Type {
	case "answer":
		if len(m.SDP) > 64*1024 {
			return errors.New("answer too large")
		}
		return c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: m.SDP})
	case "candidate":
		if m.Candidate == nil {
			return nil
		}
		return c.pc.AddICECandidate(*m.Candidate)
	case "bye":
		c.Close()
		return nil
	case "renegotiate":
		return c.sendOffer()
	default:
		return fmt.Errorf("unknown signal type %q", m.Type)
	}
}

// Done is closed when the client has been torn down.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close tears down the peer connection and virtual input devices.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
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
		c.log.Info("client disconnected", "clients", c.hub.ClientCount(), "dropped_input", c.dropped)
	})
}

func (c *Client) onInput(dc *webrtc.DataChannel, data []byte) {
	if !c.limiter.Allow() {
		c.dropped++
		if c.dropped == 1 || c.dropped%1000 == 0 {
			c.log.Warn("input rate limit exceeded; dropping messages", "dropped", c.dropped)
		}
		return
	}
	m, err := protocol.Decode(data)
	if err != nil {
		c.log.Debug("bad input message", "error", err, "len", len(data))
		return
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
func drainRTCP(s *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		if _, _, err := s.Read(buf); err != nil {
			return
		}
	}
}

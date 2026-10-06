// Package stream owns the WebRTC side of Windstream: a shared media hub that
// runs the capture pipelines while clients are connected, and per-client peer
// connections carrying video, audio and input.
package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/coral-coder/windstream/internal/config"
	"github.com/coral-coder/windstream/internal/input"
	wsmedia "github.com/coral-coder/windstream/internal/media"
)

// Config is the hub configuration assembled by the serve command.
type Config struct {
	Video wsmedia.VideoConfig
	// Source resolves the capture target before each pipeline start (DXGI
	// indices can change when monitors are added or rearranged).
	Source func() (wsmedia.Source, error)
	// Acquire/Release bracket each capture session (first viewer joins ..
	// last viewer leaves), e.g. to switch the virtual screen on only while
	// someone is streaming. Optional.
	Acquire      func(ctx context.Context) error
	Release      func()
	Audio        wsmedia.AudioConfig
	AudioEnabled bool
	WebRTC       config.WebRTC
	Input        input.Options
	InputEnabled bool
	MaxClients   int
	// StopDelay keeps the encoder running briefly after the last client leaves
	// so a page refresh does not pay the pipeline start-up cost.
	StopDelay time.Duration
}

// Stats is a point-in-time snapshot for the UI / logs.
type Stats struct {
	Error      string `json:"error,omitempty"`
	AudioError string `json:"audio_error,omitempty"`
	Clients    int    `json:"clients"`
	Encoder    string `json:"encoder"`
	Running    bool   `json:"running"`
	Frames     uint64 `json:"frames"`
	Keyframes  uint64 `json:"keyframes"`
	Bytes      uint64 `json:"bytes"`
}

// Hub multiplexes one capture pipeline to every connected client.
type Hub struct {
	cfg Config
	log *slog.Logger
	ctx context.Context

	api        *webrtc.API
	iceServers []webrtc.ICEServer
	video      *webrtc.TrackLocalStaticRTP
	audio      *webrtc.TrackLocalStaticSample
	udp        *net.UDPConn
	tcp        net.Listener

	mu         sync.Mutex
	clients    map[*Client]struct{}
	encoder    string
	candidates []string
	lastErr    string
	audioErr   string
	pipeCancel context.CancelFunc
	pipeWG     sync.WaitGroup
	stopTimer  *time.Timer

	frames, keyframes, bytes atomic.Uint64
	frameIsKey               bool
}

// NewHub prepares the WebRTC API, media tracks and ICE sockets. The capture
// pipelines start lazily when the first client connects.
func NewHub(ctx context.Context, cfg Config, log *slog.Logger) (*Hub, error) {
	if cfg.StopDelay == 0 {
		cfg.StopDelay = 15 * time.Second
	}
	h := &Hub{cfg: cfg, log: log, ctx: ctx, clients: make(map[*Client]struct{})}

	se := webrtc.SettingEngine{}
	udp, err := net.ListenUDP("udp", &net.UDPAddr{Port: cfg.WebRTC.UDPPort})
	if err != nil {
		return nil, fmt.Errorf("listen udp %d for ICE: %w", cfg.WebRTC.UDPPort, err)
	}
	h.udp = udp
	se.SetICEUDPMux(webrtc.NewICEUDPMux(nil, udp))
	networks := []webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6}
	if cfg.WebRTC.TCPPort > 0 {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.WebRTC.TCPPort))
		if err != nil {
			udp.Close()
			return nil, fmt.Errorf("listen tcp %d for ICE: %w", cfg.WebRTC.TCPPort, err)
		}
		h.tcp = ln
		se.SetICETCPMux(webrtc.NewICETCPMux(nil, ln, 32))
		networks = append(networks, webrtc.NetworkTypeTCP4, webrtc.NetworkTypeTCP6)
	}
	se.SetNetworkTypes(networks)
	if len(cfg.WebRTC.PublicIPs) > 0 {
		// Advertise the public IP as a server-reflexive candidate alongside
		// the LAN host candidates, so both remote and local clients connect
		// directly.
		se.SetNAT1To1IPs(cfg.WebRTC.PublicIPs, webrtc.ICECandidateTypeSrflx)
	}
	if cfg.WebRTC.IncludeLoopbackCandidate() {
		se.SetIncludeLoopbackCandidate(true)
	}
	se.SetICETimeouts(5*time.Second, 25*time.Second, 2*time.Second)

	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		h.closeSockets()
		return nil, err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
		h.closeSockets()
		return nil, err
	}
	h.api = webrtc.NewAPI(webrtc.WithSettingEngine(se), webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(ir))

	h.video, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeH264,
		ClockRate: 90000,
		// Constrained Baseline is what every browser offers, so it is the
		// signalled profile; WebRTC H.264 decoders in Chrome, Edge, Safari
		// and Firefox decode Main/High bitstreams regardless.
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	}, "video", "windstream")
	if err != nil {
		h.closeSockets()
		return nil, err
	}
	if cfg.AudioEnabled {
		h.audio, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1;stereo=1",
		}, "audio", "windstream")
		if err != nil {
			h.closeSockets()
			return nil, err
		}
	}
	for _, s := range cfg.WebRTC.STUNServers {
		h.iceServers = append(h.iceServers, webrtc.ICEServer{URLs: []string{s}})
	}
	return h, nil
}

func (h *Hub) closeSockets() {
	if h.udp != nil {
		h.udp.Close()
	}
	if h.tcp != nil {
		h.tcp.Close()
	}
}

// Close stops pipelines and disconnects every client.
func (h *Hub) Close() {
	h.mu.Lock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.Close()
	}
	h.mu.Lock()
	h.stopPipelinesLocked()
	h.mu.Unlock()
	h.pipeWG.Wait()
	h.closeSockets()
}

// Stats returns a snapshot.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{
		Error:      h.lastErr,
		AudioError: h.audioErr,
		Clients:    len(h.clients), Encoder: h.encoder, Running: h.pipeCancel != nil,
		Frames: h.frames.Load(), Keyframes: h.keyframes.Load(), Bytes: h.bytes.Load(),
	}
}

// ClientCount returns the number of attached clients.
func (h *Hub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *Hub) addClient(c *Client) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= h.cfg.MaxClients {
		return fmt.Errorf("stream: client limit (%d) reached", h.cfg.MaxClients)
	}
	h.clients[c] = struct{}{}
	if h.stopTimer != nil {
		h.stopTimer.Stop()
		h.stopTimer = nil
	}
	if h.pipeCancel == nil {
		h.startPipelinesLocked()
	}
	return nil
}

func (h *Hub) removeClient(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	if len(h.clients) == 0 && h.pipeCancel != nil && h.stopTimer == nil {
		h.stopTimer = time.AfterFunc(h.cfg.StopDelay, func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.stopTimer = nil
			if len(h.clients) == 0 {
				h.stopPipelinesLocked()
			}
		})
	}
}

func (h *Hub) startPipelinesLocked() {
	ctx, cancel := context.WithCancel(h.ctx)
	h.pipeCancel = cancel
	h.pipeWG.Add(1)
	go h.runVideo(ctx)
	if h.cfg.AudioEnabled {
		h.pipeWG.Add(1)
		go h.runAudio(ctx)
	}
}

func (h *Hub) stopPipelinesLocked() {
	if h.pipeCancel != nil {
		h.log.Info("stopping capture pipelines")
		h.pipeCancel()
		h.pipeCancel = nil
	}
}

// recoverPipeline keeps a Go panic in a capture pipeline (e.g. a display or
// encoder edge case) from crashing the whole process; the pipeline simply
// stops and the error shows on the dashboard.
func (h *Hub) recoverPipeline(which string) {
	if r := recover(); r != nil {
		h.log.Error("capture pipeline crashed (recovered)", "pipeline", which, "panic", r, "stack", string(debug.Stack()))
		h.mu.Lock()
		h.lastErr = fmt.Sprintf("%s pipeline error: %v", which, r)
		h.mu.Unlock()
	}
}

func (h *Hub) runVideo(ctx context.Context) {
	defer h.pipeWG.Done()
	defer h.recoverPipeline("video")
	backoff := time.Second
	if h.cfg.Acquire != nil {
		for {
			err := h.cfg.Acquire(ctx)
			if err == nil {
				break
			}
			h.mu.Lock()
			h.lastErr = err.Error()
			h.mu.Unlock()
			h.log.Error("display unavailable", "error", err)
			if !h.sleep(ctx, &backoff) {
				return
			}
		}
		if h.cfg.Release != nil {
			defer h.cfg.Release()
		}
		backoff = time.Second
	}
	h.mu.Lock()
	candidates := h.candidates // probed once, reused across viewer sessions
	h.mu.Unlock()
	next := 0
	for ctx.Err() == nil {
		if next >= len(candidates) {
			var failed map[string]error
			candidates, failed = wsmedia.Candidates(ctx, h.cfg.Video)
			h.mu.Lock()
			h.candidates = candidates
			h.mu.Unlock()
			for enc, err := range failed {
				h.log.Debug("encoder unavailable", "encoder", enc, "error", err)
			}
			next = 0
			if len(candidates) == 0 {
				h.log.Error("no working H.264 encoder found (check `windstream check`)")
				if !h.sleep(ctx, &backoff) {
					return
				}
				continue
			}
		}
		enc := candidates[next]
		src, err := h.cfg.Source()
		if err != nil {
			h.log.Error("capture target unavailable", "error", err)
			if !h.sleep(ctx, &backoff) {
				return
			}
			continue
		}
		h.mu.Lock()
		h.encoder = enc
		h.lastErr = ""
		h.mu.Unlock()
		h.log.Info("starting video pipeline", "encoder", enc, "test_source", src.Test,
			"adapter", src.Adapter, "output", src.Output, "fps", h.cfg.Video.FPS,
			"bitrate_kbps", h.cfg.Video.BitrateKbps)
		start := time.Now()
		before := h.frames.Load()
		err = wsmedia.RunVideo(ctx, h.cfg.Video, enc, src, h.log, h.onVideoPacket)
		if ctx.Err() != nil {
			return
		}
		produced := h.frames.Load() > before
		h.log.Error("video pipeline stopped", "encoder", enc, "error", err, "uptime", time.Since(start).Round(time.Millisecond))
		if !produced {
			// This encoder cannot handle this capture path (e.g. the output
			// is on a different GPU vendor); fall through to the next one.
			next++
			if next < len(candidates) {
				h.log.Warn("trying next encoder", "encoder", candidates[next])
				continue
			}
			// Every candidate failed on this capture path: re-probe next round.
			h.mu.Lock()
			h.candidates = nil
			h.mu.Unlock()
		} else {
			backoff = time.Second
		}
		if !h.sleep(ctx, &backoff) {
			return
		}
	}
}

func (h *Hub) sleep(ctx context.Context, backoff *time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(*backoff):
	}
	if *backoff < 30*time.Second {
		*backoff *= 2
	}
	return true
}

func (h *Hub) onVideoPacket(p wsmedia.VideoPacket) {
	// Only the video goroutine calls this, so frameIsKey needs no lock.
	if p.Keyframe && !h.frameIsKey {
		h.frameIsKey = true
		if h.keyframes.Add(1) == 1 {
			h.log.Info("first keyframe produced")
		}
	}
	if p.Packet.Marker {
		h.frames.Add(1)
		h.frameIsKey = false
	}
	h.bytes.Add(uint64(len(p.Packet.Payload)))
	if err := h.video.WriteRTP(p.Packet); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		h.log.Debug("write video rtp", "error", err)
	}
}

func (h *Hub) runAudio(ctx context.Context) {
	defer h.pipeWG.Done()
	defer h.recoverPipeline("audio")
	backoff := 2 * time.Second
	lastErr := ""
	for ctx.Err() == nil {
		start := time.Now()
		err := wsmedia.RunAudio(ctx, h.cfg.Audio, h.log, func(s wsmedia.AudioSample) {
			h.mu.Lock()
			h.audioErr = ""
			h.mu.Unlock()
			_ = h.audio.WriteSample(media.Sample{Data: s.Data, Duration: s.Duration})
		})
		if ctx.Err() != nil {
			return
		}
		msg := "unknown error"
		if err != nil {
			msg = err.Error()
		}
		// Log and record only when the failure first appears or changes, so a
		// box with no audio endpoint does not spam the log while retrying.
		if msg != lastErr {
			h.log.Warn("audio unavailable (video continues)", "error", msg)
			lastErr = msg
		}
		h.mu.Lock()
		h.audioErr = msg
		h.mu.Unlock()
		if time.Since(start) > 30*time.Second {
			backoff = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

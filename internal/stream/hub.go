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
	"slices"
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
	"github.com/coral-coder/windstream/internal/safe"
)

// Config is the hub configuration assembled by the serve command.
type Config struct {
	Video wsmedia.VideoConfig
	// Codec forces a video codec (av1, h265, h264) when every viewer can
	// decode it; "auto" or "" picks per viewer, most efficient first.
	Codec string
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
	Codec      string `json:"codec"`
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
	tracks     map[string]*webrtc.TrackLocalStaticRTP // one per codec
	audio      *webrtc.TrackLocalStaticSample
	udp        *net.UDPConn
	tcp        net.Listener

	mu         sync.Mutex
	closed     bool
	clients    map[*Client]struct{}
	codec      string          // video codec being streamed ("" while idle)
	lastPrefs  []string        // codec preferences of the newest viewer
	broken     map[string]bool // codecs no encoder could capture with this session
	encoder    string
	lastErr    string
	audioErr   string
	pipeCancel context.CancelFunc
	runCancel  context.CancelFunc // stops the current encoder run (codec switch)
	pipeWG     sync.WaitGroup
	pipeDone   chan struct{} // closed when the latest pipeline set has fully stopped
	stopTimer  *time.Timer

	probeMu sync.Mutex // serializes encoder probing
	availMu sync.Mutex
	avail   map[string][]string // codec -> working vendors, in preference order

	frames, keyframes, bytes atomic.Uint64
	// pktMu guards per-packet state: a stopped pipeline's goroutine can
	// deliver its last packets while a restarted one begins.
	pktMu      sync.Mutex
	frameIsKey bool
	rewriter   *rtpRewriter
}

// NewHub prepares the WebRTC API, media tracks and ICE sockets. The capture
// pipelines start lazily when the first client connects.
func NewHub(ctx context.Context, cfg Config, log *slog.Logger) (*Hub, error) {
	if cfg.StopDelay == 0 {
		cfg.StopDelay = 15 * time.Second
	}
	h := &Hub{cfg: cfg, log: log, ctx: ctx, clients: make(map[*Client]struct{}),
		broken: map[string]bool{}, avail: map[string][]string{},
		tracks: map[string]*webrtc.TrackLocalStaticRTP{}, rewriter: newRTPRewriter()}

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
	if err := me.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: playoutDelayURI}, webrtc.RTPCodecTypeVideo); err != nil {
		h.closeSockets()
		return nil, err
	}
	ir.Add(playoutDelayFactory{})
	h.api = webrtc.NewAPI(webrtc.WithSettingEngine(se), webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(ir))

	// Video and audio deliberately use different stream IDs: browsers
	// lip-sync tracks of the same stream by delaying whichever arrives
	// first, which would hold every video frame back by the audio jitter
	// buffer. For a game, late audio beats late video.
	for codec, cap := range map[string]webrtc.RTPCodecCapability{
		// Constrained Baseline is what every browser offers, so it is the
		// signalled profile; WebRTC H.264 decoders in Chrome, Edge, Safari
		// and Firefox decode Main/High bitstreams regardless.
		wsmedia.CodecH264: {MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"},
		wsmedia.CodecH265: {MimeType: webrtc.MimeTypeH265, ClockRate: 90000},
		wsmedia.CodecAV1:  {MimeType: webrtc.MimeTypeAV1, ClockRate: 90000},
	} {
		t, err := webrtc.NewTrackLocalStaticRTP(cap, "video", "windstream-video")
		if err != nil {
			h.closeSockets()
			return nil, err
		}
		h.tracks[codec] = t
	}
	if cfg.AudioEnabled {
		h.audio, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1;stereo=1",
		}, "audio", "windstream-audio")
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
	h.closed = true // no new clients from here on
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
		Clients:    len(h.clients), Encoder: h.encoder, Codec: h.codec, Running: h.pipeCancel != nil,
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
	if h.closed {
		return errors.New("stream: the streaming engine is restarting")
	}
	if len(h.clients) >= h.cfg.MaxClients {
		return fmt.Errorf("stream: client limit (%d) reached", h.cfg.MaxClients)
	}
	h.clients[c] = struct{}{}
	if h.stopTimer != nil {
		h.stopTimer.Stop()
		h.stopTimer = nil
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
			defer safe.Recover(h.log, "pipeline stop timer")
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
	prev, done := h.pipeDone, make(chan struct{})
	h.pipeDone = done
	h.pipeWG.Add(1)
	go func() {
		defer h.pipeWG.Done()
		defer close(done)
		// A viewer reconnecting right after the last one left must not
		// start a second capture and encoder while the previous ones are
		// still shutting down: they would fight over the display and the
		// GPU encoder, and interleave packets on the same track.
		if prev != nil {
			<-prev
		}
		if ctx.Err() != nil {
			return
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go h.keepRunning(ctx, &wg, "video", h.runVideo)
		if h.cfg.AudioEnabled {
			wg.Add(1)
			go h.keepRunning(ctx, &wg, "audio", h.runAudio)
		}
		wg.Wait()
	}()
}

func (h *Hub) stopPipelinesLocked() {
	if h.pipeCancel != nil {
		h.log.Info("stopping capture pipelines")
		h.pipeCancel()
		h.pipeCancel = nil
	}
	// The next session negotiates from scratch (and retries any codec that
	// failed this time: the display may simply have been asleep).
	h.codec = ""
	h.broken = map[string]bool{}
}

// keepRunning runs a capture pipeline until ctx ends. A Go panic inside it
// (a display, driver or encoder edge case) is logged and the pipeline is
// started again a second later, instead of crashing the process or leaving
// viewers with a frozen stream.
func (h *Hub) keepRunning(ctx context.Context, wg *sync.WaitGroup, which string, run func(context.Context)) {
	defer wg.Done()
	for ctx.Err() == nil {
		if !safe.Call(h.log, which+" pipeline", func() { run(ctx) }) {
			return // returned normally: ctx is done
		}
		h.mu.Lock()
		h.lastErr = which + " pipeline hit an internal error and was restarted"
		h.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (h *Hub) runVideo(ctx context.Context) {
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
	next, lastCodec := 0, ""
	for ctx.Err() == nil {
		h.mu.Lock()
		codec := h.codec
		h.mu.Unlock()
		if codec == "" {
			return // pipelines are being stopped
		}
		if codec != lastCodec {
			next, lastCodec = 0, codec
		}
		candidates := h.encodersFor(ctx, codec)
		if next >= len(candidates) {
			// Every encoder for this codec failed on this capture path.
			if h.codecFailed(codec) {
				continue // viewers were moved to another codec
			}
			h.forgetEncoders(codec) // re-probe next round
			next = 0
			if len(candidates) == 0 {
				h.log.Error("no working video encoder found (check `windstream check`)", "codec", codec)
			}
			if !h.sleep(ctx, &backoff) {
				return
			}
			continue
		}
		vendor := candidates[next]
		src, err := h.cfg.Source()
		if err != nil {
			h.log.Error("capture target unavailable", "error", err)
			if !h.sleep(ctx, &backoff) {
				return
			}
			continue
		}
		runCtx, cancel := context.WithCancel(ctx)
		h.mu.Lock()
		if h.codec != codec { // switched while probing
			h.mu.Unlock()
			cancel()
			continue
		}
		h.encoder = wsmedia.EncoderName(codec, vendor)
		h.lastErr = ""
		h.runCancel = cancel
		h.mu.Unlock()
		h.log.Info("starting video pipeline", "codec", codec, "encoder", wsmedia.EncoderName(codec, vendor),
			"test_source", src.Test, "adapter", src.Adapter, "output", src.Output, "fps", h.cfg.Video.FPS,
			"bitrate_kbps", h.cfg.Video.BitrateKbps)
		start := time.Now()
		before := h.frames.Load()
		track := h.tracks[codec]
		h.pktMu.Lock()
		h.frameIsKey = false
		h.rewriter.restart()
		h.pktMu.Unlock()
		err = wsmedia.RunVideo(runCtx, h.cfg.Video, codec, vendor, src, h.log, func(p wsmedia.VideoPacket) {
			h.onVideoPacket(track, p)
		})
		cancel()
		h.mu.Lock()
		h.runCancel = nil
		switched := h.codec != codec
		h.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if switched {
			h.log.Info("video codec changed; restarting encoder", "from", codec)
			continue
		}
		produced := h.frames.Load() > before
		h.log.Error("video pipeline stopped", "encoder", wsmedia.EncoderName(codec, vendor), "error", err,
			"uptime", time.Since(start).Round(time.Millisecond))
		if !produced {
			// This encoder cannot handle this capture path (e.g. the output
			// is on a different GPU vendor); fall through to the next one.
			next++
			if next < len(candidates) {
				h.log.Warn("trying next encoder", "encoder", wsmedia.EncoderName(codec, candidates[next]))
				continue
			}
			continue // exhausted: handled at the top of the loop
		}
		backoff = time.Second
		if !h.sleep(ctx, &backoff) {
			return
		}
	}
}

// encodersFor returns the vendors that can encode codec here, probing them
// once (the result is cached for the life of the hub).
func (h *Hub) encodersFor(ctx context.Context, codec string) []string {
	if codec == "" {
		return nil
	}
	h.availMu.Lock()
	v, ok := h.avail[codec]
	h.availMu.Unlock()
	if ok {
		return v
	}
	h.probeMu.Lock()
	defer h.probeMu.Unlock()
	h.availMu.Lock()
	v, ok = h.avail[codec]
	h.availMu.Unlock()
	if ok {
		return v
	}
	v, failed := wsmedia.Candidates(ctx, h.cfg.Video, codec)
	for vendor, err := range failed {
		h.log.Debug("encoder unavailable", "codec", codec, "vendor", vendor, "error", err)
	}
	h.log.Info("video encoders probed", "codec", codec, "working", v)
	// Do not cache an empty result: ffmpeg may still be downloading, or the
	// probe was cancelled.
	if len(v) > 0 && ctx.Err() == nil {
		h.availMu.Lock()
		h.avail[codec] = v
		h.availMu.Unlock()
	}
	return v
}

func (h *Hub) forgetEncoders(codec string) {
	h.availMu.Lock()
	delete(h.avail, codec)
	h.availMu.Unlock()
}

func (h *Hub) knownEncoders(codec string) []string {
	h.availMu.Lock()
	defer h.availMu.Unlock()
	return h.avail[codec]
}

// probeCodecs probes every codec in prefs concurrently so the first viewer
// does not wait for them one by one.
func (h *Hub) probeCodecs(ctx context.Context, prefs []string) {
	var wg sync.WaitGroup
	for _, c := range prefs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer safe.Recover(h.log, "encoder probe")
			h.encodersFor(ctx, c)
		}()
	}
	wg.Wait()
}

// ErrNoCodec means the viewer's browser cannot decode any codec this server
// can encode.
var ErrNoCodec = errors.New("no common video codec")

// normalizeCodecs filters a browser's codec list to known codecs, without
// duplicates. Old clients send nothing: they only ever handled H.264.
func normalizeCodecs(prefs []string) []string {
	var out []string
	for _, c := range prefs {
		if wsmedia.ValidCodec(c) && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		out = []string{wsmedia.CodecH264}
	}
	return out
}

// attach chooses the video codec for a viewer, adds the matching video track
// to its peer connection and starts the capture pipelines if needed.
func (h *Hub) attach(c *Client, prefs []string) error {
	prefs = normalizeCodecs(prefs)
	h.probeCodecs(h.ctx, prefs)
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok || h.closed {
		return errors.New("client closed")
	}
	c.codecs = prefs
	codec := h.codec
	if codec == "" || !slices.Contains(prefs, codec) {
		codec = h.pickLocked(prefs)
	}
	if codec == "" {
		c.codecs = nil
		return ErrNoCodec
	}
	sender, err := c.pc.AddTrack(h.tracks[codec])
	if err != nil {
		c.codecs = nil
		return fmt.Errorf("add video track: %w", err)
	}
	c.videoSender = sender
	go drainRTCP(c.log, sender)
	h.lastPrefs = prefs
	c.log.Info("video codec chosen", "codec", codec, "browser_supports", prefs)
	if codec != h.codec {
		h.switchCodecLocked(codec, c)
	}
	if h.pipeCancel == nil {
		h.startPipelinesLocked()
	}
	return nil
}

// pickLocked returns the best codec that every attached viewer can decode
// and this machine can encode: hardware encoders in the newest viewer's
// order of preference, then software H.264, then any software encoder.
func (h *Hub) pickLocked(prefs []string) string {
	usable := func(codec string, hwOnly bool) bool {
		if h.broken[codec] {
			return false
		}
		for cl := range h.clients {
			if cl.codecs != nil && !slices.Contains(cl.codecs, codec) {
				return false
			}
		}
		for _, v := range h.knownEncoders(codec) {
			if !hwOnly || wsmedia.IsHardware(v) {
				return true
			}
		}
		return false
	}
	if f := h.cfg.Codec; wsmedia.ValidCodec(f) && slices.Contains(prefs, f) && usable(f, false) {
		return f
	}
	for _, codec := range prefs {
		if usable(codec, true) {
			return codec
		}
	}
	if slices.Contains(prefs, wsmedia.CodecH264) && usable(wsmedia.CodecH264, false) {
		return wsmedia.CodecH264
	}
	for _, codec := range prefs {
		if usable(codec, false) {
			return codec
		}
	}
	return ""
}

// switchCodecLocked moves every viewer (except skip, already set up) to
// codec and restarts the encoder.
func (h *Hub) switchCodecLocked(codec string, skip *Client) {
	if h.codec != "" {
		h.log.Info("switching video codec", "from", h.codec, "to", codec)
	}
	h.codec = codec
	for cl := range h.clients {
		if cl == skip || cl.videoSender == nil {
			continue
		}
		if err := cl.videoSender.ReplaceTrack(h.tracks[codec]); err != nil {
			cl.log.Warn("viewer cannot switch video codec; disconnecting it", "codec", codec, "error", err)
			safe.Go(cl.log, "close viewer", cl.Close)
		}
	}
	if h.runCancel != nil {
		h.runCancel()
	}
}

// codecFailed is called when no encoder could stream codec. It moves the
// viewers to the next best codec and reports whether it did.
func (h *Hub) codecFailed(codec string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.codec != codec {
		return true // already switched
	}
	h.broken[codec] = true
	next := h.pickLocked(h.lastPrefs)
	if next == "" || next == codec {
		delete(h.broken, codec)
		return false
	}
	h.log.Warn("no encoder could stream this codec here; falling back", "codec", codec, "fallback", next)
	h.switchCodecLocked(next, nil)
	return true
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

func (h *Hub) onVideoPacket(track *webrtc.TrackLocalStaticRTP, p wsmedia.VideoPacket) {
	// Held across the write so sequence numbers go out in order.
	h.pktMu.Lock()
	defer h.pktMu.Unlock()
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
	h.rewriter.rewrite(p.Packet)
	if err := track.WriteRTP(p.Packet); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		h.log.Debug("write video rtp", "error", err)
	}
}

func (h *Hub) runAudio(ctx context.Context) {
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

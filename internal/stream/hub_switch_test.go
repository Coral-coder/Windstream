package stream

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/coral-coder/windstream/internal/config"
	wsmedia "github.com/coral-coder/windstream/internal/media"
)

// testViewer is a minimal pion "browser" that records the codec of every
// video packet it receives.
type testViewer struct {
	pc     *webrtc.PeerConnection
	client *Client
	mu     sync.Mutex
	pend   []webrtc.ICECandidateInit
	counts map[string]int // mime -> packets
	first  map[string]time.Time
}

func newTestViewer(t *testing.T, hub *Hub, name string, codecs []string) *testViewer {
	t.Helper()
	v := &testViewer{counts: map[string]int{}, first: map[string]time.Time{}}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	v.pc = pc
	t.Cleanup(func() { pc.Close() })
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			if _, _, err := tr.ReadRTP(); err != nil {
				return
			}
			v.mu.Lock()
			if v.counts[tr.Codec().MimeType]++; v.counts[tr.Codec().MimeType] == 1 {
				v.first[tr.Codec().MimeType] = time.Now()
			}
			v.mu.Unlock()
		}
	})
	ready := make(chan struct{})
	send := func(m SignalMessage) error {
		switch m.Type {
		case "offer":
			if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}); err != nil {
				return err
			}
			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				return err
			}
			if err := pc.SetLocalDescription(answer); err != nil {
				return err
			}
			v.mu.Lock()
			for _, c := range v.pend {
				_ = pc.AddICECandidate(c)
			}
			v.pend = nil
			v.mu.Unlock()
			go func() {
				<-ready
				if err := v.client.HandleSignal(SignalMessage{Type: "answer", SDP: answer.SDP}); err != nil {
					t.Errorf("%s: answer rejected: %v", name, err)
				}
			}()
		case "candidate":
			v.mu.Lock()
			defer v.mu.Unlock()
			if pc.RemoteDescription() == nil {
				v.pend = append(v.pend, *m.Candidate)
				return nil
			}
			return pc.AddICECandidate(*m.Candidate)
		}
		return nil
	}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		<-ready
		init := c.ToJSON()
		_ = v.client.HandleSignal(SignalMessage{Type: "candidate", Candidate: &init})
	})
	c, err := hub.Connect(name, "127.0.0.1", send)
	if err != nil {
		t.Fatal(err)
	}
	v.client = c
	close(ready)
	t.Cleanup(c.Close)
	if err := c.HandleSignal(SignalMessage{Type: "hello", Codecs: codecs}); err != nil {
		t.Fatalf("%s hello: %v", name, err)
	}
	return v
}

func (v *testViewer) count(mime string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.counts[mime]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHubSwitchesCodecForNewViewer: viewer A (AV1 or H.264) gets H.264;
// viewer B only decodes AV1, so the hub moves everyone to AV1 live, without
// renegotiating A.
func TestHubSwitchesCodecForNewViewer(t *testing.T) {
	ff := testFFmpeg(t)
	out, _ := exec.Command(ff, "-version").Output()
	if !regexp.MustCompile(`ffmpeg version n?([89]|\d\d)\.`).Match(out) {
		t.Skip("needs ffmpeg 8+ for AV1 RTP (set WINDSTREAM_TEST_FFMPEG)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	hub, err := NewHub(ctx, Config{
		Video: wsmedia.VideoConfig{FFmpeg: ff, Width: 640, Height: 360, FPS: 30,
			BitrateKbps: 1500, GOPFrames: 30, Encoder: "x264"},
		Source:     func() (wsmedia.Source, error) { return wsmedia.Source{Test: true}, nil },
		WebRTC:     config.WebRTC{UDPPort: 0, IncludeLoopback: true},
		MaxClients: 3,
		StopDelay:  100 * time.Millisecond,
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	a := newTestViewer(t, hub, "a", []string{"av1", "h264"})
	waitFor(t, "H.264 at viewer A", func() bool { return a.count(webrtc.MimeTypeH264) > 30 })
	if st := hub.Stats(); st.Codec != "h264" {
		t.Fatalf("codec %q, want h264", st.Codec)
	}

	t0 := time.Now()
	b := newTestViewer(t, hub, "b", []string{"av1"})
	waitFor(t, "AV1 at viewer B", func() bool { return b.count(webrtc.MimeTypeAV1) > 30 })
	waitFor(t, "AV1 at viewer A after the switch", func() bool { return a.count(webrtc.MimeTypeAV1) > 30 })
	t.Logf("first AV1 packet: B after %v, A after %v", b.first[webrtc.MimeTypeAV1].Sub(t0), a.first[webrtc.MimeTypeAV1].Sub(t0))
	if st := hub.Stats(); st.Codec != "av1" || st.Encoder != "libsvtav1" || st.Clients != 2 {
		t.Fatalf("stats after switch: %+v", st)
	}

	// A viewer that shares no codec with the others is refused cleanly.
	c, err := hub.Connect("c", "127.0.0.1", func(SignalMessage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.HandleSignal(SignalMessage{Type: "hello", Codecs: []string{"h265"}}); err == nil {
		t.Fatal("viewer with no common codec was accepted")
	}
}

func TestPickCodec(t *testing.T) {
	h := &Hub{clients: map[*Client]struct{}{}, broken: map[string]bool{},
		avail: map[string][]string{"av1": {"nvenc"}, "h265": {"nvenc"}, "h264": {"nvenc", "x264"}}}
	add := func(codecs ...string) *Client {
		c := &Client{codecs: codecs}
		h.clients[c] = struct{}{}
		return c
	}
	if got := h.pickLocked([]string{"av1", "h265", "h264"}); got != "av1" {
		t.Errorf("hardware AV1 first: got %q", got)
	}
	if got := h.pickLocked([]string{"h264", "av1"}); got != "h264" {
		t.Errorf("browser order wins among hardware codecs: got %q", got)
	}
	h.cfg.Codec = "h265"
	if got := h.pickLocked([]string{"av1", "h265", "h264"}); got != "h265" {
		t.Errorf("forced codec: got %q", got)
	}
	if got := h.pickLocked([]string{"av1", "h264"}); got != "av1" {
		t.Errorf("forced codec the browser lacks must be ignored: got %q", got)
	}
	h.cfg.Codec = "auto"
	add("h264", "h265")
	if got := h.pickLocked([]string{"av1", "h265", "h264"}); got != "h265" {
		t.Errorf("must be decodable by every viewer: got %q", got)
	}
	h.broken["h265"] = true
	if got := h.pickLocked([]string{"av1", "h265", "h264"}); got != "h264" {
		t.Errorf("broken codec skipped: got %q", got)
	}

	// Software only: H.264 beats software AV1; software AV1 is the last resort.
	h = &Hub{clients: map[*Client]struct{}{}, broken: map[string]bool{},
		avail: map[string][]string{"av1": {"x264"}, "h264": {"x264"}}}
	if got := h.pickLocked([]string{"av1", "h264"}); got != "h264" {
		t.Errorf("software: got %q", got)
	}
	if got := h.pickLocked([]string{"av1"}); got != "av1" {
		t.Errorf("software AV1 last resort: got %q", got)
	}
	if got := h.pickLocked([]string{"h265"}); got != "" {
		t.Errorf("no encoder: got %q", got)
	}
	if got := normalizeCodecs([]string{"vp8", "av1", "av1"}); len(got) != 1 || got[0] != "av1" {
		t.Errorf("normalize: %v", got)
	}
	if got := normalizeCodecs(nil); len(got) != 1 || got[0] != "h264" {
		t.Errorf("old clients get h264: %v", got)
	}
}

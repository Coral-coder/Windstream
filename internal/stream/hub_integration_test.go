package stream

import (
	"context"
	"encoding/binary"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/coral-coder/windstream/internal/config"
	"github.com/coral-coder/windstream/internal/input"
	wsmedia "github.com/coral-coder/windstream/internal/media"
	"github.com/coral-coder/windstream/internal/protocol"
)

// testFFmpeg returns $WINDSTREAM_TEST_FFMPEG or ffmpeg from PATH.
func testFFmpeg(t *testing.T) string {
	t.Helper()
	ff := os.Getenv("WINDSTREAM_TEST_FFMPEG")
	if ff == "" {
		ff = "ffmpeg"
	}
	if _, err := exec.LookPath(ff); err != nil {
		t.Skip("ffmpeg not installed")
	}
	return ff
}

func TestHubEndToEnd(t *testing.T) {
	t.Run("h264", func(t *testing.T) {
		runHubEndToEnd(t, nil, webrtc.MimeTypeH264, "libx264")
	})
	t.Run("av1", func(t *testing.T) {
		ff := testFFmpeg(t)
		out, _ := exec.Command(ff, "-version").Output()
		if !regexp.MustCompile(`ffmpeg version n?([89]|\d\d)\.`).Match(out) {
			t.Skip("needs ffmpeg 8+ for AV1 RTP (set WINDSTREAM_TEST_FFMPEG)")
		}
		// A browser that only decodes AV1 (like Playwright's Chromium,
		// which has no H.264) gets software AV1.
		runHubEndToEnd(t, []string{"av1"}, webrtc.MimeTypeAV1, "libsvtav1")
	})
	t.Run("prefers hardware-free h264 over software av1", func(t *testing.T) {
		runHubEndToEnd(t, []string{"av1", "h264"}, webrtc.MimeTypeH264, "libx264")
	})
}

// runHubEndToEnd stands a pion peer in for the browser: it says hello with
// codecs, answers the hub's offer over loopback ICE, checks that RTP of the
// expected codec arrives from a live ffmpeg pipeline (synthetic source) with
// the zero playout-delay extension, and round-trips a ping over the input
// channel.
func runHubEndToEnd(t *testing.T, codecs []string, wantMime, wantEncoder string) {
	ff := testFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	hub, err := NewHub(ctx, Config{
		Video: wsmedia.VideoConfig{FFmpeg: ff, Width: 640, Height: 360, FPS: 30,
			BitrateKbps: 1500, GOPFrames: 30, Encoder: "x264"},
		Source:       func() (wsmedia.Source, error) { return wsmedia.Source{Test: true}, nil },
		AudioEnabled: false,
		WebRTC:       config.WebRTC{UDPPort: 0, IncludeLoopback: true},
		Input:        input.Options{Gamepads: true, Keyboard: true, Mouse: true, MaxGamepads: 2},
		InputEnabled: false,
		MaxClients:   1,
		StopDelay:    100 * time.Millisecond,
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	// "Browser" side, which (like Chrome) understands playout-delay.
	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	if err := me.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: playoutDelayURI}, webrtc.RTPCodecTypeVideo); err != nil {
		t.Fatal(err)
	}
	browser, err := webrtc.NewAPI(webrtc.WithMediaEngine(me)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	var packets, delayed atomic.Int32
	gotVideo := make(chan struct{}, 1)
	browser.OnTrack(func(tr *webrtc.TrackRemote, rcv *webrtc.RTPReceiver) {
		if tr.Codec().MimeType != wantMime {
			t.Errorf("codec %s, want %s", tr.Codec().MimeType, wantMime)
		}
		if tr.StreamID() != "windstream-video" {
			t.Errorf("video stream id %q: must differ from audio so browsers do not lip-sync", tr.StreamID())
		}
		var extID uint8
		for _, e := range rcv.GetParameters().HeaderExtensions {
			if e.URI == playoutDelayURI {
				extID = uint8(e.ID)
			}
		}
		for {
			p, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			if ext := p.GetExtension(extID); extID != 0 && len(ext) == 3 && ext[0] == 0 && ext[1] == 0 && ext[2] == 0 {
				delayed.Add(1)
			}
			if packets.Add(1) == 60 {
				gotVideo <- struct{}{}
			}
		}
	})
	pong := make(chan uint32, 1)
	browser.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "control" {
			return
		}
		dc.OnOpen(func() {
			_ = dc.Send([]byte{protocol.MsgPing, 0x78, 0x56, 0x34, 0x12})
		})
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			if len(m.Data) == 5 && m.Data[0] == protocol.MsgPong {
				pong <- binary.LittleEndian.Uint32(m.Data[1:])
			}
		})
	})

	var client *Client
	var mu sync.Mutex
	pendingCands := []webrtc.ICECandidateInit{}
	send := func(m SignalMessage) error {
		switch m.Type {
		case "offer":
			if err := browser.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}); err != nil {
				return err
			}
			answer, err := browser.CreateAnswer(nil)
			if err != nil {
				return err
			}
			if err := browser.SetLocalDescription(answer); err != nil {
				return err
			}
			mu.Lock()
			for _, c := range pendingCands {
				_ = browser.AddICECandidate(c)
			}
			pendingCands = nil
			mu.Unlock()
			go func() {
				mu.Lock()
				c := client
				mu.Unlock()
				for c == nil { // Connect has not returned yet
					time.Sleep(10 * time.Millisecond)
					mu.Lock()
					c = client
					mu.Unlock()
				}
				if err := c.HandleSignal(SignalMessage{Type: "answer", SDP: answer.SDP}); err != nil {
					t.Errorf("answer rejected: %v", err)
				}
			}()
		case "candidate":
			mu.Lock()
			defer mu.Unlock()
			if browser.RemoteDescription() == nil {
				pendingCands = append(pendingCands, *m.Candidate)
				return nil
			}
			return browser.AddICECandidate(*m.Candidate)
		}
		return nil
	}
	browser.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		mu.Lock()
		cl := client
		mu.Unlock()
		if cl != nil {
			init := c.ToJSON()
			_ = cl.HandleSignal(SignalMessage{Type: "candidate", Candidate: &init})
		}
	})

	c, err := hub.Connect("tester", "127.0.0.1", send)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	client = c
	mu.Unlock()
	defer c.Close()
	if codecs != nil {
		if err := c.HandleSignal(SignalMessage{Type: "hello", Codecs: codecs}); err != nil {
			t.Fatal(err)
		}
	} // else: an old client; the hub falls back to H.264 after helloTimeout

	if _, err := hub.Connect("second", "127.0.0.1", func(SignalMessage) error { return nil }); err == nil {
		t.Error("second client should be refused by max_clients=1")
	}

	select {
	case <-gotVideo:
		t.Logf("received %d %s RTP packets", packets.Load(), wantMime)
		if delayed.Load() != packets.Load() && delayed.Load() < 60 {
			t.Errorf("only %d of %d packets carry playout-delay 0", delayed.Load(), packets.Load())
		}
	case <-ctx.Done():
		t.Fatalf("no video RTP received (state=%s, packets=%d)", browser.ConnectionState(), packets.Load())
	}
	select {
	case ts := <-pong:
		if ts != 0x12345678 {
			t.Errorf("pong echoed %#x", ts)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no pong on control channel")
	}
	st := hub.Stats()
	if st.Clients != 1 || !st.Running || st.Encoder != wantEncoder || st.Frames == 0 {
		t.Errorf("stats = %+v", st)
	}

	c.Close()
	time.Sleep(300 * time.Millisecond)
	if st := hub.Stats(); st.Clients != 0 || st.Running {
		t.Errorf("pipeline should stop after last client leaves: %+v", st)
	}
}

package stream

import (
	"context"
	"encoding/binary"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
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
	runHubEndToEndWith(t, codecs, wantMime, wantEncoder, e2eOptions{})
}

type e2eOptions struct {
	// dropEvery makes the "browser" lose every Nth video packet, so it
	// sends NACKs and the hub retransmits (RTX), for this long.
	dropEvery int
	hold      time.Duration
	// ffmpeg, if set, wraps the real ffmpeg (returns the program to run).
	ffmpeg func(t *testing.T, real string) string
	// notices receives every "notice" message sent to the viewer.
	notices chan string
}

func runHubEndToEndWith(t *testing.T, codecs []string, wantMime, wantEncoder string, opt e2eOptions) {
	ff := testFFmpeg(t)
	if opt.ffmpeg != nil {
		ff = opt.ffmpeg(t, ff)
	}
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
	ir := &interceptor.Registry{}
	var dropped atomic.Int32
	if opt.dropEvery > 0 {
		// Registered first, so the NACK generator after it sees the gaps.
		ir.Add(&lossFactory{every: opt.dropEvery, dropped: &dropped})
		if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
			t.Fatal(err)
		}
	}
	browser, err := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(ir)).NewPeerConnection(webrtc.Configuration{})
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
		case "notice":
			if opt.notices != nil {
				opt.notices <- m.Message
			}
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
	if opt.hold > 0 {
		time.Sleep(opt.hold)
		t.Logf("dropped %d packets; %d delivered in %s", dropped.Load(), packets.Load(), opt.hold)
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

// lossFactory drops every Nth incoming video RTP packet on the receiving
// side, like a lossy Wi-Fi or mobile link.
type lossFactory struct {
	every   int
	dropped *atomic.Int32
}

func (f *lossFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &lossInterceptor{f: f}, nil
}

type lossInterceptor struct {
	interceptor.NoOp
	f *lossFactory
	n atomic.Int64
}

func (l *lossInterceptor) BindRemoteStream(info *interceptor.StreamInfo, r interceptor.RTPReader) interceptor.RTPReader {
	if info.MimeType == "" || info.MimeType[:5] != "video" {
		return r
	}
	return interceptor.RTPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		for {
			n, attrs, err := r.Read(b, a)
			if err != nil || l.n.Add(1)%int64(l.f.every) != 0 {
				return n, attrs, err
			}
			l.f.dropped.Add(1)
		}
	})
}

func TestHubRetransmitsUnderLoss(t *testing.T) {
	// The crash reports from Oct 8 and Oct 12 died in the NACK responder
	// while retransmitting video; exercise that path under the race
	// detector for several seconds.
	runHubEndToEndWith(t, []string{"h264"}, webrtc.MimeTypeH264, "libx264", e2eOptions{dropEvery: 7, hold: 8 * time.Second})
}

func TestHubWaitsOutAdministratorPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	// The first capture attempts fail the way ddagrab does while Windows
	// shows a UAC prompt (the Oct 12 log). The viewer must stay connected,
	// be told why, and get the picture back on the same codec and encoder.
	wrap := func(t *testing.T, real string) string {
		dir := t.TempDir()
		script := filepath.Join(dir, "ffmpeg")
		body := "#!/bin/sh\n" +
			"case \"$*\" in *testsrc2*)\n" +
			"  n=$(cat \"" + dir + "/count\" 2>/dev/null || echo 0); n=$((n+1)); echo $n > \"" + dir + "/count\"\n" +
			"  if [ $n -le 3 ]; then\n" +
			"    echo \"[Parsed_ddagrab_0 @ 0000020df00f0b00] Desktop duplication access denied\" >&2\n" +
			"    echo \"[fc#0 @ 0000020e345b20c0] Error configuring filter graph: Operation not permitted\" >&2\n" +
			"    exit 1\n" +
			"  fi;;\n" +
			"esac\n" +
			"exec \"" + real + "\" \"$@\"\n"
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return script
	}
	notices := make(chan string, 32)
	runHubEndToEndWith(t, []string{"h264"}, webrtc.MimeTypeH264, "libx264", e2eOptions{ffmpeg: wrap, notices: notices})
	var got []string
	deadline := time.After(3 * time.Second)
	for len(got) == 0 || got[len(got)-1] != "" {
		select {
		case n := <-notices:
			got = append(got, n)
		case <-deadline:
			t.Fatalf("notices %q: want the prompt explained, then cleared", got)
		}
	}
	if !slices.Contains(got, blockedNotice) {
		t.Errorf("notices %q: viewer was not told about the prompt", got)
	}
}

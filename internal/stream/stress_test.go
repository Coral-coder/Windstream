package stream

import (
	"context"
	"log/slog"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/coral-coder/windstream/internal/config"
	"github.com/coral-coder/windstream/internal/input"
	wsmedia "github.com/coral-coder/windstream/internal/media"
	"github.com/coral-coder/windstream/internal/protocol"
)

// TestHubStress hammers the hub the way flaky networks and impatient users
// do: viewers joining and leaving at random, mixed codec lists, malformed
// and out-of-order signaling, renegotiation storms and garbage on the input
// channels. The hub must neither crash, deadlock nor leak viewers.
func TestHubStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}
	ff := testFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	hub, err := NewHub(ctx, Config{
		Video: wsmedia.VideoConfig{FFmpeg: ff, Width: 320, Height: 240, FPS: 30,
			BitrateKbps: 800, GOPFrames: 30, Encoder: "x264", Preset: "ultrafast"},
		Source:       func() (wsmedia.Source, error) { return wsmedia.Source{Test: true}, nil },
		WebRTC:       config.WebRTC{UDPPort: 0, IncludeLoopback: true},
		Input:        input.Options{Gamepads: true, Keyboard: true, Mouse: true, MaxGamepads: 4},
		InputEnabled: true, // falls back to view-only off Windows; exercises the path
		MaxClients:   4,
		StopDelay:    50 * time.Millisecond,
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	codecSets := [][]string{nil, {"h264"}, {"av1", "h264"}, {"h265", "h264"}, {"bogus"}, {"h265"}}
	junk := []SignalMessage{
		{Type: "renegotiate"}, {Type: "candidate"}, {Type: "answer", SDP: "garbage"},
		{Type: "nonsense"}, {Type: "hello", Codecs: []string{"h264"}}, {Type: "hello"},
		{Type: "candidate", Candidate: &webrtc.ICECandidateInit{Candidate: "candidate:junk"}},
	}
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 15 && ctx.Err() == nil; i++ {
				v := &stressViewer{t: t}
				if err := v.connect(hub, codecSets[rng.Intn(len(codecSets))]); err != nil {
					continue // client limit or no common codec: expected
				}
				for j := 0; j < rng.Intn(6); j++ {
					_ = v.client.HandleSignal(junk[rng.Intn(len(junk))])
				}
				// Garbage and valid input. Pings are answered on an
				// unopened channel, as when the browser goes away mid-ping.
				dc, _ := v.pc.CreateDataChannel("probe", nil)
				for j := 0; j < 50; j++ {
					b := make([]byte, rng.Intn(40))
					rng.Read(b)
					if len(b) > 0 && rng.Intn(2) == 0 {
						b[0] = []byte{protocol.MsgGamepadState, protocol.MsgKey, protocol.MsgMouseMove, protocol.MsgPing, protocol.MsgGamepadConnect}[rng.Intn(5)]
					}
					v.client.onInput(dc, b)
				}
				time.Sleep(time.Duration(rng.Intn(800)) * time.Millisecond)
				if rng.Intn(2) == 0 {
					_ = v.client.HandleSignal(SignalMessage{Type: "bye"})
				}
				v.close()
			}
		}(int64(w))
	}
	wg.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for hub.ClientCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := hub.ClientCount(); n != 0 {
		t.Fatalf("%d viewers leaked", n)
	}
	// The hub still works after the abuse.
	v := newTestViewer(t, hub, "after", []string{"h264"})
	waitFor(t, "video after stress", func() bool { return v.count(webrtc.MimeTypeH264) > 20 })
}

type stressViewer struct {
	t      *testing.T
	pc     *webrtc.PeerConnection
	client *Client
	mu     sync.Mutex
}

func (v *stressViewer) connect(hub *Hub, codecs []string) error {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	v.pc = pc
	ready := make(chan struct{})
	send := func(m SignalMessage) error {
		if m.Type != "offer" {
			return nil
		}
		if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}); err != nil {
			return nil // a renegotiation racing another: fine
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			return nil
		}
		_ = pc.SetLocalDescription(answer)
		go func() {
			<-ready
			_ = v.client.HandleSignal(SignalMessage{Type: "answer", SDP: answer.SDP})
		}()
		return nil
	}
	c, err := hub.Connect("stress", "127.0.0.1", send)
	if err != nil {
		pc.Close()
		return err
	}
	v.client = c
	close(ready)
	if codecs != nil {
		_ = c.HandleSignal(SignalMessage{Type: "hello", Codecs: codecs})
	}
	return nil
}

func (v *stressViewer) close() {
	v.client.Close()
	v.pc.Close()
}

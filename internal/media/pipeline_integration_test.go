package media

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

// TestVideoPipelineTestSource runs the real ffmpeg -> RTP pipeline with the
// synthetic source and checks complete frames and keyframes arrive.
func TestVideoPipelineTestSource(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := VideoConfig{FFmpeg: "ffmpeg", Width: 640, Height: 360, FPS: 30,
		BitrateKbps: 1500, GOPFrames: 15, Encoder: "x264", Preset: "ultrafast"}
	var frames, keys, packets atomic.Int32
	runCtx, stop := context.WithCancel(ctx)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	done := make(chan error, 1)
	go func() {
		done <- RunVideo(runCtx, cfg, "x264", Source{Test: true}, log, func(p VideoPacket) {
			packets.Add(1)
			if p.Packet.PayloadType != 96 {
				t.Errorf("payload type %d", p.Packet.PayloadType)
			}
			if p.Keyframe {
				keys.Add(1)
			}
			if p.Packet.Marker && frames.Add(1) >= 45 {
				stop()
			}
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("pipeline: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for frames")
	}
	if frames.Load() < 45 || keys.Load() < 3 {
		t.Fatalf("frames=%d keyframes=%d packets=%d", frames.Load(), keys.Load(), packets.Load())
	}
	t.Logf("%d frames, %d keyframe packets, %d RTP packets", frames.Load(), keys.Load(), packets.Load())
}

func TestProbeEncoder(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if err := ProbeEncoder(context.Background(), "ffmpeg", "x264"); err != nil {
		t.Fatalf("x264 probe failed: %v", err)
	}
	if err := ProbeEncoder(context.Background(), "ffmpeg", "bogus"); err == nil {
		t.Fatal("bogus encoder accepted")
	}
}

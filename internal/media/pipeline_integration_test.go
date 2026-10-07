package media

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// TestVideoPipelineTestSource runs the real ffmpeg -> RTP pipeline with the
// synthetic source for each software-encodable codec and checks complete
// frames and keyframes arrive.
func TestVideoPipelineTestSource(t *testing.T) {
	for _, tc := range []struct{ codec, preset string }{{CodecH264, "ultrafast"}, {CodecAV1, "12"}} {
		t.Run(tc.codec, func(t *testing.T) {
			ff := testFFmpeg(t)
			needEncoder(t, ff, EncoderName(tc.codec, "x264"))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cfg := VideoConfig{FFmpeg: ff, Width: 640, Height: 360, FPS: 30,
				BitrateKbps: 1500, GOPFrames: 15, Encoder: "x264", Preset: tc.preset}
			var frames, keys, packets atomic.Int32
			runCtx, stop := context.WithCancel(ctx)
			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
			done := make(chan error, 1)
			go func() {
				done <- RunVideo(runCtx, cfg, tc.codec, "x264", Source{Test: true}, log, func(p VideoPacket) {
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
		})
	}
}

// TestHEVCKeyframeDetection packetizes real HEVC (libx265, standing in for
// the GPU encoders) and checks keyframes are found exactly at GOP starts.
func TestHEVCKeyframeDetection(t *testing.T) {
	ff := testFFmpeg(t)
	needEncoder(t, ff, "libx265")
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadBuffer(8 << 20)
	type result struct{ frames, keyFrames int }
	got := make(chan result, 1)
	go func() {
		// Read concurrently: ffmpeg encodes far faster than real time.
		buf := make([]byte, 1600)
		frames, keyFrames, inKey := 0, 0, false
		for {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				break
			}
			p := &rtp.Packet{}
			if p.Unmarshal(buf[:n]) != nil {
				continue
			}
			if IsKeyframeStart(CodecH265, p) && !inKey {
				if frames%15 != 0 {
					t.Errorf("keyframe at frame %d, want multiples of 15", frames)
				}
				inKey = true
				keyFrames++
			}
			if p.Marker {
				frames++
				inKey = false
			}
		}
		got <- result{frames, keyFrames}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-re", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30", "-frames:v", "45", "-pix_fmt", "yuv420p",
		"-g", "15", "-bf", "0", "-c:v", "libx265", "-preset", "ultrafast", "-tune", "zerolatency",
		"-x265-params", "repeat-headers=1:log-level=error", "-profile:v", "main",
		"-f", "rtp", "-payload_type", "96",
		fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", conn.LocalAddr().(*net.UDPAddr).Port))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	r := <-got
	frames, keyFrames := r.frames, r.keyFrames
	if frames != 45 || keyFrames != 3 {
		t.Fatalf("frames=%d keyframes=%d (want 45/3)", frames, keyFrames)
	}
}

func TestProbeEncoder(t *testing.T) {
	ff := testFFmpeg(t)
	needEncoder(t, ff, "libx264")
	if err := ProbeEncoder(context.Background(), ff, CodecH264, "x264"); err != nil {
		t.Fatalf("x264 probe failed: %v", err)
	}
	if err := ProbeEncoder(context.Background(), ff, CodecH264, "bogus"); err == nil {
		t.Fatal("bogus encoder accepted")
	}
	if err := ProbeEncoder(context.Background(), ff, CodecH265, "x264"); err == nil {
		t.Fatal("software HEVC must not be offered")
	}
}

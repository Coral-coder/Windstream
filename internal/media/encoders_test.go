package media

import (
	"strings"
	"testing"
)

func TestBuildVideoArgsDesktop(t *testing.T) {
	cfg := VideoConfig{Width: 1920, Height: 1080, FPS: 60, BitrateKbps: 15000, GOPFrames: 120, ShowCursor: true}
	src := Source{Adapter: 1, Output: 2}
	for _, enc := range EncoderOrder {
		args := BuildVideoArgs(cfg, enc, src, "rtp://127.0.0.1:5004?pkt_size=1200")
		j := strings.Join(args, " ")
		for _, want := range []string{
			"-init_hw_device d3d11va=ws:1", "-filter_hw_device ws",
			"ddagrab=output_idx=2:framerate=60:draw_mouse=1",
			"-c:v " + encoderCodec[enc], "-g 120", "-bf 0", "-b:v 15000k",
			"-f rtp -payload_type 96 rtp://127.0.0.1:5004?pkt_size=1200",
		} {
			if !strings.Contains(j, want) {
				t.Errorf("%s: missing %q in: %s", enc, want, j)
			}
		}
	}
	x264 := strings.Join(BuildVideoArgs(cfg, "x264", src, "rtp://x"), " ")
	if !strings.Contains(x264, "hwdownload,format=bgra") {
		t.Errorf("x264 must download frames from the GPU: %s", x264)
	}
	nv := strings.Join(BuildVideoArgs(cfg, "nvenc", src, "rtp://x"), " ")
	if strings.Contains(nv, "hwdownload") {
		t.Errorf("nvenc must stay zero-copy: %s", nv)
	}
}

func TestBuildVideoArgsTest(t *testing.T) {
	cfg := VideoConfig{Width: 640, Height: 360, FPS: 30, BitrateKbps: 2000, GOPFrames: 30}
	j := strings.Join(BuildVideoArgs(cfg, "x264", Source{Test: true}, "rtp://x"), " ")
	if !strings.Contains(j, "testsrc2=size=640x360:rate=30") || strings.Contains(j, "ddagrab") {
		t.Errorf("test source args wrong: %s", j)
	}
}

func TestBuildAudioArgs(t *testing.T) {
	j := strings.Join(BuildAudioArgs(AudioConfig{Backend: "dshow", Device: "CABLE Output", BitrateKbps: 96}, nil), " ")
	for _, want := range []string{"-f dshow", "-i audio=CABLE Output", "-c:a libopus", "-b:a 96k", "-frame_duration 10", "-f ogg"} {
		if !strings.Contains(j, want) {
			t.Errorf("dshow args missing %q: %s", want, j)
		}
	}
	j = strings.Join(BuildAudioArgs(AudioConfig{Backend: "wasapi", BitrateKbps: 128}, &PCMFormat{Float: true, Rate: 44100, Channels: 2}), " ")
	if !strings.Contains(j, "-f f32le -ar 44100 -ac 2 -i pipe:0") || !strings.Contains(j, "-ar 48000") {
		t.Errorf("wasapi args wrong: %s", j)
	}
}

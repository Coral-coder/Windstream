package media

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseAVFoundationDevices(t *testing.T) {
	out := []byte(`[AVFoundation indev @ 0x7f8] AVFoundation video devices:
[AVFoundation indev @ 0x7f8] [0] FaceTime HD Camera
[AVFoundation indev @ 0x7f8] [1] Capture screen 0
[AVFoundation indev @ 0x7f8] [2] Capture screen 1
[AVFoundation indev @ 0x7f8] AVFoundation audio devices:
[AVFoundation indev @ 0x7f8] [0] MacBook Pro Microphone
[AVFoundation indev @ 0x7f8] [1] BlackHole 2ch
: Input/output error
`)
	video, audio := parseAVFoundationDevices(out)
	if !reflect.DeepEqual(video, []string{"FaceTime HD Camera", "Capture screen 0", "Capture screen 1"}) {
		t.Errorf("video = %q", video)
	}
	if !reflect.DeepEqual(audio, []string{"MacBook Pro Microphone", "BlackHole 2ch"}) {
		t.Errorf("audio = %q", audio)
	}
	if got := PickLoopback(audio); got != "BlackHole 2ch" {
		t.Errorf("loopback = %q", got)
	}
	if got := PickLoopback([]string{"MacBook Pro Microphone"}); got != "" {
		t.Errorf("a microphone was picked as loopback: %q", got)
	}
}

func TestMacVideoArgs(t *testing.T) {
	cfg := VideoConfig{FFmpeg: "ffmpeg", Width: 1920, Height: 1080, FPS: 60, BitrateKbps: 15000, GOPFrames: 120, ShowCursor: true}
	args := strings.Join(BuildVideoArgs(cfg, CodecH265, "videotoolbox", Source{Screen: "Capture screen 0"}, "rtp://127.0.0.1:5000"), " ")
	for _, want := range []string{
		"-f avfoundation", "-capture_cursor 1", "-i Capture screen 0:none",
		"scale=1920:1080:force_original_aspect_ratio=decrease", "-pix_fmt nv12",
		"-c:v hevc_videotoolbox", "-realtime 1", "-allow_sw 0", "-profile:v main",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	if strings.Contains(args, "ddagrab") || strings.Contains(args, "d3d11") {
		t.Errorf("Windows capture in Mac args: %s", args)
	}
	if got := encoderOrder("darwin"); got[0] != "videotoolbox" {
		t.Errorf("mac encoder order %v", got)
	}
	if EncoderName(CodecAV1, "videotoolbox") != "" {
		t.Error("VideoToolbox cannot encode AV1")
	}
}

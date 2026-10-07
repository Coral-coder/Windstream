// Package media runs the ffmpeg capture/encode pipelines (Desktop Duplication
// capture, GPU H.264 encode, Opus audio) and hands their output to WebRTC.
package media

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// Source says what to capture.
type Source struct {
	// Test selects a synthetic test pattern instead of desktop capture.
	Test bool
	// Adapter is the DXGI adapter index; Output is the output index on it
	// (ffmpeg ddagrab output_idx).
	Adapter int
	Output  int
}

// VideoConfig is everything the video pipeline needs.
type VideoConfig struct {
	FFmpeg      string
	Width       int // test source size (desktop capture uses the output's mode)
	Height      int
	FPS         int
	BitrateKbps int
	GOPFrames   int
	Encoder     string // auto|nvenc|amf|qsv|x264
	Preset      string
	Profile     string // baseline|main|high
	ShowCursor  bool
	ExtraArgs   []string
}

// EncoderOrder is the preference used by "auto": every GPU vendor's hardware
// encoder first (they also keep frames on the GPU), software last.
var EncoderOrder = []string{"nvenc", "amf", "qsv", "x264"}

var encoderCodec = map[string]string{
	"nvenc": "h264_nvenc", "amf": "h264_amf", "qsv": "h264_qsv", "x264": "libx264",
}

// ProbeEncoder runs a tiny test encode to prove an encoder works on this
// machine (ffmpeg build + driver + GPU), not just that ffmpeg lists it.
func ProbeEncoder(ctx context.Context, ffmpeg, encoder string) error {
	codec, ok := encoderCodec[encoder]
	if !ok {
		return fmt.Errorf("unknown encoder %q", encoder)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "color=black:s=320x240:r=30", "-frames:v", "3",
		"-pix_fmt", "nv12", "-c:v", codec, "-f", "null", "-")
	configureCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := string(out)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("%s: %v %s", codec, err, msg)
	}
	return nil
}

// Candidates returns the encoders to try, in order.
func Candidates(ctx context.Context, cfg VideoConfig) ([]string, map[string]error) {
	if cfg.Encoder != "auto" && cfg.Encoder != "" {
		return []string{cfg.Encoder}, nil
	}
	var ok []string
	failed := map[string]error{}
	for _, e := range EncoderOrder {
		if err := ProbeEncoder(ctx, cfg.FFmpeg, e); err != nil {
			failed[e] = err
			continue
		}
		ok = append(ok, e)
	}
	return ok, failed
}

// BuildVideoArgs assembles the ffmpeg command line. Output is RTP to
// rtpURL; the RTP marker bit tells us exactly when a frame is complete, so no
// frame is held back waiting for the next one.
func BuildVideoArgs(cfg VideoConfig, encoder string, src Source, rtpURL string) []string {
	kbps := strconv.Itoa(cfg.BitrateKbps) + "k"
	// One frame of rate-control buffer: the encoder may not run ahead, which
	// is the lowest-latency setting (at a small cost to quality on very busy
	// frames). This is a game streamer, so latency wins.
	vbvKbps := cfg.BitrateKbps / cfg.FPS
	if vbvKbps < 500 {
		vbvKbps = 500
	}
	vbv := strconv.Itoa(vbvKbps) + "k"
	gop := strconv.Itoa(cfg.GOPFrames)
	fps := strconv.Itoa(cfg.FPS)
	cursor := "0"
	if cfg.ShowCursor {
		cursor = "1"
	}
	profile := cfg.Profile
	if profile == "" {
		profile = "high"
	}
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-nostats",
		"-fflags", "nobuffer", "-flags", "low_delay"}

	if src.Test {
		args = append(args, "-re", "-f", "lavfi", "-i",
			fmt.Sprintf("testsrc2=size=%dx%d:rate=%d", cfg.Width, cfg.Height, cfg.FPS))
		switch encoder {
		case "amf", "qsv":
			args = append(args, "-pix_fmt", "nv12")
		default:
			args = append(args, "-pix_fmt", "yuv420p")
		}
	} else {
		// Desktop Duplication straight into a D3D11 texture on the GPU that
		// drives the captured output. Hardware encoders consume that texture
		// directly (zero copy); only x264 downloads frames to system memory.
		grab := fmt.Sprintf("ddagrab=output_idx=%d:framerate=%s:draw_mouse=%s", src.Output, fps, cursor)
		switch encoder {
		case "qsv":
			grab += ",hwmap=derive_device=qsv,format=qsv,vpp_qsv=format=nv12"
		case "x264":
			grab += ",hwdownload,format=bgra"
		}
		args = append(args,
			"-init_hw_device", fmt.Sprintf("d3d11va=ws:%d", src.Adapter), "-filter_hw_device", "ws",
			"-filter_complex", grab)
		if encoder == "x264" {
			args = append(args, "-pix_fmt", "yuv420p")
		}
	}

	args = append(args, "-an", "-g", gop, "-keyint_min", gop, "-bf", "0",
		"-b:v", kbps, "-maxrate", kbps, "-bufsize", vbv, "-profile:v", profile)
	preset := cfg.Preset
	switch encoder {
	case "nvenc":
		if preset == "" {
			preset = "p1"
		}
		args = append(args, "-c:v", "h264_nvenc", "-preset", preset, "-tune", "ull", "-rc", "cbr",
			"-zerolatency", "1", "-delay", "0", "-forced-idr", "1", "-no-scenecut", "1",
			"-rc-lookahead", "0", "-multipass", "0", "-b_ref_mode", "0")
	case "amf":
		if preset == "" {
			preset = "speed"
		}
		args = append(args, "-c:v", "h264_amf", "-usage", "ultralowlatency", "-quality", preset,
			"-rc", "cbr", "-header_spacing", gop)
	case "qsv":
		if preset == "" {
			preset = "veryfast"
		}
		args = append(args, "-c:v", "h264_qsv", "-preset", preset, "-look_ahead", "0", "-async_depth", "1",
			"-low_power", "1")
	default:
		if preset == "" {
			preset = "superfast"
		}
		args = append(args, "-c:v", "libx264", "-preset", preset, "-tune", "zerolatency",
			"-x264-params", "repeat-headers=1:nal-hrd=cbr:force-cfr=1:sliced-threads=1")
	}
	args = append(args, cfg.ExtraArgs...)
	args = append(args, "-flush_packets", "1", "-f", "rtp", "-payload_type", "96", rtpURL)
	return args
}

// Package media runs the ffmpeg capture/encode pipelines (Desktop Duplication
// capture, GPU AV1/HEVC/H.264 encode, Opus audio) and hands their output to
// WebRTC.
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
	Encoder     string // auto|nvenc|amf|qsv|x264 (x264 = software)
	Preset      string
	Profile     string // H.264 profile: baseline|main|high
	ShowCursor  bool
	ExtraArgs   []string
}

// Video codecs, as negotiated with browsers.
const (
	CodecAV1  = "av1"
	CodecH265 = "h265"
	CodecH264 = "h264"
)

// Codecs lists every codec Windstream can stream, most efficient first.
var Codecs = []string{CodecAV1, CodecH265, CodecH264}

// ValidCodec reports whether c is a known codec name.
func ValidCodec(c string) bool { return c == CodecAV1 || c == CodecH265 || c == CodecH264 }

// EncoderOrder is the vendor preference used by "auto": every GPU vendor's
// hardware encoder first (they also keep frames on the GPU), software last.
// "x264" is the historical name of the software vendor.
var EncoderOrder = []string{"nvenc", "amf", "qsv", "x264"}

// IsHardware reports whether a vendor encodes on the GPU.
func IsHardware(vendor string) bool { return vendor != "x264" }

// ffmpegEncoders maps codec -> vendor -> ffmpeg encoder name. Software HEVC
// is deliberately missing: x265 cannot keep up with 60 fps game streaming on
// ordinary CPUs. Software AV1 (SVT-AV1) is only used when the viewer's
// browser can decode nothing else (the hub ranks it last).
var ffmpegEncoders = map[string]map[string]string{
	CodecH264: {"nvenc": "h264_nvenc", "amf": "h264_amf", "qsv": "h264_qsv", "x264": "libx264"},
	CodecH265: {"nvenc": "hevc_nvenc", "amf": "hevc_amf", "qsv": "hevc_qsv"},
	CodecAV1:  {"nvenc": "av1_nvenc", "amf": "av1_amf", "qsv": "av1_qsv", "x264": "libsvtav1"},
}

// EncoderName returns the ffmpeg encoder for codec on vendor, or "".
func EncoderName(codec, vendor string) string { return ffmpegEncoders[codec][vendor] }

// ProbeEncoder runs a tiny test encode to prove an encoder works on this
// machine (ffmpeg build + driver + GPU), not just that ffmpeg lists it.
func ProbeEncoder(ctx context.Context, ffmpeg, codec, vendor string) error {
	name := EncoderName(codec, vendor)
	if name == "" {
		return fmt.Errorf("no %s encoder for %q", codec, vendor)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// 320x240 is below some GPUs' AV1 minimum; 640x360 is safe everywhere.
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "color=black:s=640x360:r=30", "-frames:v", "3",
		"-pix_fmt", "nv12", "-c:v", name, "-f", "null", "-")
	configureCmd(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := string(out)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("%s: %v %s", name, err, msg)
	}
	return nil
}

// Candidates returns the vendors whose encoder for codec works here, in
// preference order, plus why the others failed.
func Candidates(ctx context.Context, cfg VideoConfig, codec string) ([]string, map[string]error) {
	vendors := EncoderOrder
	if cfg.Encoder != "auto" && cfg.Encoder != "" {
		vendors = []string{cfg.Encoder}
	}
	var ok []string
	failed := map[string]error{}
	for _, v := range vendors {
		if EncoderName(codec, v) == "" {
			continue
		}
		if err := ProbeEncoder(ctx, cfg.FFmpeg, codec, v); err != nil {
			failed[v] = err
			continue
		}
		ok = append(ok, v)
	}
	return ok, failed
}

// BuildVideoArgs assembles the ffmpeg command line. Output is RTP to
// rtpURL; the RTP marker bit tells us exactly when a frame is complete, so no
// frame is held back waiting for the next one.
func BuildVideoArgs(cfg VideoConfig, codec, vendor string, src Source, rtpURL string) []string {
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
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-nostats",
		"-fflags", "nobuffer", "-flags", "low_delay"}

	software := !IsHardware(vendor)
	if src.Test {
		args = append(args, "-re", "-f", "lavfi", "-i",
			fmt.Sprintf("testsrc2=size=%dx%d:rate=%d", cfg.Width, cfg.Height, cfg.FPS))
		switch vendor {
		case "amf", "qsv":
			args = append(args, "-pix_fmt", "nv12")
		default:
			args = append(args, "-pix_fmt", "yuv420p")
		}
	} else {
		// Desktop Duplication straight into a D3D11 texture on the GPU that
		// drives the captured output. Hardware encoders consume that texture
		// directly (zero copy); only software encoders download frames.
		grab := fmt.Sprintf("ddagrab=output_idx=%d:framerate=%s:draw_mouse=%s", src.Output, fps, cursor)
		switch {
		case vendor == "qsv":
			grab += ",hwmap=derive_device=qsv,format=qsv,vpp_qsv=format=nv12"
		case software:
			grab += ",hwdownload,format=bgra"
		}
		args = append(args,
			"-init_hw_device", fmt.Sprintf("d3d11va=ws:%d", src.Adapter), "-filter_hw_device", "ws",
			"-filter_complex", grab)
		if software {
			args = append(args, "-pix_fmt", "yuv420p")
		}
	}

	args = append(args, "-an", "-g", gop, "-keyint_min", gop, "-bf", "0", "-b:v", kbps)
	if !(software && codec == CodecAV1) {
		// SVT-AV1 rejects maxrate == bitrate; its low-delay CBR mode keeps
		// its own one-frame-ish buffer.
		args = append(args, "-maxrate", kbps, "-bufsize", vbv)
	}
	switch codec {
	case CodecH264:
		profile := cfg.Profile
		if profile == "" {
			profile = "high"
		}
		args = append(args, "-profile:v", profile)
	case CodecH265:
		args = append(args, "-profile:v", "main")
	}
	// AV1: every encoder defaults to Main, the profile browsers decode.

	preset := cfg.Preset
	enc := EncoderName(codec, vendor)
	args = append(args, "-c:v", enc)
	switch vendor {
	case "nvenc":
		if preset == "" {
			preset = "p1"
		}
		args = append(args, "-preset", preset, "-tune", "ull", "-rc", "cbr",
			"-zerolatency", "1", "-delay", "0", "-forced-idr", "1", "-no-scenecut", "1",
			"-rc-lookahead", "0", "-multipass", "0", "-b_ref_mode", "0")
	case "amf":
		if preset == "" {
			preset = "speed"
		}
		args = append(args, "-usage", "ultralowlatency", "-quality", preset, "-rc", "cbr")
		switch codec {
		case CodecH264:
			args = append(args, "-latency", "1", "-header_spacing", gop)
		case CodecH265:
			args = append(args, "-latency", "1", "-header_insertion_mode", "idr")
		case CodecAV1:
			args = append(args, "-latency", "lowest_latency", "-header_insertion_mode", "frame")
		}
	case "qsv":
		if preset == "" {
			preset = "veryfast"
		}
		args = append(args, "-preset", preset, "-async_depth", "1", "-low_power", "1")
		if codec == CodecH264 {
			args = append(args, "-look_ahead", "0")
		}
	default:
		switch codec {
		case CodecAV1:
			if preset == "" {
				preset = "12"
			}
			// Low-delay prediction structure (no lookahead), CBR.
			args = append(args, "-preset", preset,
				"-svtav1-params", "pred-struct=1:rc=2")
		default:
			if preset == "" {
				preset = "superfast"
			}
			args = append(args, "-preset", preset, "-tune", "zerolatency",
				"-x264-params", "repeat-headers=1:nal-hrd=cbr:force-cfr=1:sliced-threads=1")
		}
	}
	args = append(args, cfg.ExtraArgs...)
	if codec == CodecAV1 {
		// FFmpeg flags its AV1 RTP packetizer experimental only because
		// the payload spec is still formally a draft; browsers implement
		// it as is.
		args = append(args, "-strict", "experimental")
	}
	args = append(args, "-flush_packets", "1", "-f", "rtp", "-payload_type", "96", rtpURL)
	return args
}

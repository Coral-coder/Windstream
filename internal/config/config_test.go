package config

import (
	"strings"
	"testing"
)

const minimal = `
[tls]
self_signed = true

[display]
mode = "test"

[[users]]
name = "alice"
password_hash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
`

func TestParseMinimal(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Server.Listen != ":8443" {
		t.Errorf("default listen not applied: %q", cfg.Server.Listen)
	}
	if cfg.Video.GOPFrames() != 120 {
		t.Errorf("gop frames = %d, want 120", cfg.Video.GOPFrames())
	}
	if !cfg.Input.Gamepads || !cfg.Audio.Enabled {
		t.Errorf("boolean defaults lost")
	}
}

func TestRejectsUnknownKeys(t *testing.T) {
	_, err := Parse([]byte(minimal + "\n[video]\nbitrate = 5\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown config keys") {
		t.Fatalf("expected unknown key error, got %v", err)
	}
}

func TestRequiresTLSSource(t *testing.T) {
	_, err := Parse([]byte(strings.Replace(minimal, "self_signed = true", "", 1)))
	if err == nil || !strings.Contains(err.Error(), "TLS source") {
		t.Fatalf("expected TLS error, got %v", err)
	}
}

func TestDisplayModes(t *testing.T) {
	for _, mode := range []string{"virtual", "monitor", "test"} {
		if _, err := Parse([]byte(strings.Replace(minimal, `mode = "test"`, `mode = "`+mode+`"`, 1))); err != nil {
			t.Errorf("mode %s: %v", mode, err)
		}
	}
	_, err := Parse([]byte(strings.Replace(minimal, `mode = "test"`, `mode = "xorg"`, 1)))
	if err == nil || !strings.Contains(err.Error(), "display.mode") {
		t.Fatalf("expected display.mode error, got %v", err)
	}
	cfg, _ := Parse([]byte(minimal))
	if cfg.Video.Encoder != "auto" || cfg.Audio.Backend != "wasapi" || cfg.Video.Profile != "high" {
		t.Errorf("windows defaults not applied: %+v %+v", cfg.Video, cfg.Audio)
	}
}

func TestDshowNeedsDevice(t *testing.T) {
	_, err := Parse([]byte(minimal + "\n[audio]\nbackend = \"dshow\"\n"))
	if err == nil || !strings.Contains(err.Error(), "audio.device") {
		t.Fatalf("expected audio.device error, got %v", err)
	}
}

func TestRequireTOTP(t *testing.T) {
	_, err := Parse([]byte(minimal + "\n[auth]\nrequire_totp = true\n"))
	if err == nil || !strings.Contains(err.Error(), "totp_secret") {
		t.Fatalf("expected totp error, got %v", err)
	}
}

func TestVideoCodec(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Video.Codec != "auto" {
		t.Errorf("default codec %q, want auto", cfg.Video.Codec)
	}
	if _, err := Parse([]byte(minimal + "\n[video]\ncodec = \"av1\"\n")); err != nil {
		t.Errorf("av1 rejected: %v", err)
	}
	_, err = Parse([]byte(minimal + "\n[video]\ncodec = \"vp8\"\n"))
	if err == nil || !strings.Contains(err.Error(), "video.codec") {
		t.Errorf("vp8 accepted: %v", err)
	}
}

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

func TestUnknownKeysAreIgnored(t *testing.T) {
	// A setting from a newer version (or a typo) must never stop Windstream
	// from starting; it is reported instead.
	cfg, err := Parse([]byte(minimal + "\n[video]\nbitrate = 5\nfuture_option = true\n"))
	if err != nil {
		t.Fatalf("unknown keys rejected: %v", err)
	}
	if strings.Join(cfg.UnknownKeys, ",") != "video.bitrate,video.future_option" {
		t.Fatalf("unknown keys = %v", cfg.UnknownKeys)
	}
}

func TestRepair(t *testing.T) {
	def := AppDefaults()
	user := `
[[users]]
name = "alice"
password_hash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
totp_secret = "JBSWY3DPEHPK3PXP"
`
	// An invalid value in one section: only that section is reset, the
	// account and the other settings survive.
	bad := user + "\n[video]\ncodec = \"vp8\"\nfps = 90\n[network]\ncustom_domain = \"games.example.com\"\n"
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("invalid file accepted")
	}
	cfg, what := Repair([]byte(bad), def)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("repaired config invalid: %v", err)
	}
	if len(cfg.Users) != 1 || cfg.Users[0].Name != "alice" {
		t.Fatalf("account lost: %+v", cfg.Users)
	}
	if cfg.Network.CustomDomain != "games.example.com" {
		t.Errorf("valid section was reset: %+v", cfg.Network)
	}
	if cfg.Video.Codec != "auto" || !strings.Contains(what, "video") {
		t.Errorf("video not reset (codec %q, %q)", cfg.Video.Codec, what)
	}
	// Not TOML at all: defaults.
	cfg, what = Repair([]byte("this is [not toml"), def)
	if cfg.Validate() != nil || len(cfg.Users) != 0 || what == "" {
		t.Errorf("garbage not reset to defaults: %q", what)
	}
	// A valid file comes back unchanged.
	if cfg, what := Repair([]byte(user), def); what != "" || len(cfg.Users) != 1 {
		t.Errorf("valid file changed: %q", what)
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

func TestRepairKeepsAccountsAfterTypeError(t *testing.T) {
	data := []byte(`
[video]
fps = "sixty"

[[users]]
name = "alice"
password_hash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"
totp_secret = "JBSWY3DPEHPK3PXP"

[[users]]
name = "alice"
password_hash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$b3RoZXI"
totp_secret = "JBSWY3DPEHPK3PXP"

[[users]]
name = "bob"
password_hash = "plain"
`)
	cfg, what := Repair(data, AppDefaults())
	if len(cfg.Users) != 1 || cfg.Users[0].Name != "alice" || !strings.Contains(cfg.Users[0].PasswordHash, "aGFzaA") {
		t.Fatalf("users = %+v", cfg.Users)
	}
	if !strings.Contains(what, "accounts were kept") {
		t.Fatalf("what = %q", what)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRepairDropsDuplicateUsers(t *testing.T) {
	def := AppDefaults()
	def.Users = nil
	data := []byte(`
[[users]]
name = "alice"
password_hash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"
totp_secret = "JBSWY3DPEHPK3PXP"

[[users]]
name = "alice"
password_hash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$b3RoZXI"
totp_secret = "JBSWY3DPEHPK3PXP"
`)
	cfg, what := Repair(data, def)
	if len(cfg.Users) != 1 || cfg.Validate() != nil {
		t.Fatalf("users = %+v, err = %v", cfg.Users, cfg.Validate())
	}
	if !strings.Contains(what, "Removed 1") {
		t.Fatalf("what = %q", what)
	}
}

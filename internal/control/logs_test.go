package control

import (
	"archive/zip"
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coral-coder/windstream/internal/config"
)

func TestLogsZip(t *testing.T) {
	dir := t.TempDir()
	c, err := New(Options{DataDir: dir, Dev: true, Version: "9.9.9",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Logs: NewLogBuffer(10),
		LastCrash: "panic: something broke"})
	if err != nil {
		t.Fatal(err)
	}
	const hash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const secret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	if err := c.update(func(cfg *config.Config) error {
		cfg.Users = append(cfg.Users, config.User{Name: "player", PasswordHash: hash, TOTPSecret: secret})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "logs", "windstream.log"), []byte("level=INFO msg=hello\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "logs", "crash.log"), []byte("===== crash =====\npanic: boom\n"), 0o600)

	rec := httptest.NewRecorder()
	newPanel(c).handleLogsZip(rec, httptest.NewRequest("GET", "/api/logs.zip", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".zip") {
		t.Fatalf("content disposition %q", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	if !strings.Contains(files["windstream.log"], "msg=hello") || !strings.Contains(files["crash.log"], "panic: boom") {
		t.Fatalf("logs missing from bundle: %v", keys(files))
	}
	d := files["diagnostics.txt"]
	for _, want := range []string{"9.9.9", "panic: something broke", "accounts: 1", "Settings"} {
		if !strings.Contains(d, want) {
			t.Errorf("diagnostics lacks %q:\n%s", want, d)
		}
	}
	all := strings.Join(values(files), "\n")
	for _, secretBit := range []string{secret, "argon2id", "c2FsdHNhbHRz"} {
		if strings.Contains(all, secretBit) {
			t.Errorf("log bundle leaks a secret (%q)", secretBit)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func values(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestStartsWithBrokenSettings(t *testing.T) {
	dir := t.TempDir()
	const bad = `
[[users]]
name = "player"
password_hash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
totp_secret = "JBSWY3DPEHPK3PXP"

[video]
codec = "vp9"
setting_from_the_future = 1
`
	if err := os.WriteFile(filepath.Join(dir, "windstream.toml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{DataDir: dir, Dev: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Logs: NewLogBuffer(10)})
	if err != nil {
		t.Fatalf("refused to start: %v", err)
	}
	cfg := c.Config()
	if len(cfg.Users) != 1 || cfg.Users[0].Name != "player" {
		t.Fatalf("account lost: %+v", cfg.Users)
	}
	if cfg.Video.Codec != "auto" {
		t.Errorf("invalid codec not reset: %q", cfg.Video.Codec)
	}
	if !strings.Contains(c.configNotice, "video") || !strings.Contains(c.configNotice, ".broken-") {
		t.Errorf("notice = %q", c.configNotice)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "windstream.toml.broken-*"))
	if len(backups) != 1 {
		t.Errorf("backup not kept: %v", backups)
	}
	// The repaired file loads cleanly next time.
	if _, err := New(Options{DataDir: dir, Dev: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Logs: NewLogBuffer(10)}); err != nil {
		t.Fatal(err)
	}
}

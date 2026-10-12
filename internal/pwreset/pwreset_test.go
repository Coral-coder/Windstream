package pwreset

import (
	"bufio"
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coral-coder/windstream/internal/auth"
	"github.com/coral-coder/windstream/internal/config"
)

func writeConfig(t *testing.T, users ...string) string {
	t.Helper()
	cfg := config.AppDefaults()
	for _, name := range users {
		h, err := auth.HashPassword("old-password-123")
		if err != nil {
			t.Fatal(err)
		}
		sec, _ := auth.GenerateTOTPSecret()
		cfg.Users = append(cfg.Users, config.User{Name: name, PasswordHash: h, TOTPSecret: sec})
	}
	path := filepath.Join(t.TempDir(), "windstream.toml")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func secrets(vals ...string) func(string) (string, error) {
	return func(string) (string, error) {
		v := vals[0]
		vals = vals[1:]
		return v, nil
	}
}

func TestResetPasswordKeepAuthenticator(t *testing.T) {
	path := writeConfig(t, "alice", "bob")
	before, _ := config.Load(path)
	var out bytes.Buffer
	err := Run(path, IO{
		In:     bufio.NewReader(strings.NewReader("7\nBob\ny\n")),
		Out:    &out,
		Secret: secrets("short", "brand-new-password", "typo-password-xx", "brand-new-password", "brand-new-password"),
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := auth.VerifyPassword(cfg.Users[1].PasswordHash, "brand-new-password"); !ok {
		t.Fatalf("bob's password not changed: %+v\n%s", cfg.Users, out.String())
	}
	if cfg.Users[1].TOTPSecret != before.Users[1].TOTPSecret {
		t.Fatal("authenticator changed although it was kept")
	}
	if cfg.Users[0] != before.Users[0] {
		t.Fatal("alice was touched")
	}
	for _, want := range []string{"too short", "didn't match", "Done."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestResetWithNewAuthenticator(t *testing.T) {
	path := writeConfig(t, "alice")
	now := time.Now()
	var shown string
	var out bytes.Buffer
	in := &lazyReader{out: &out, now: now}
	err := Run(path, IO{
		In:     bufio.NewReader(in),
		Out:    &out,
		Secret: secrets("brand-new-password", "brand-new-password"),
		ShowQR: func(u string) error { shown = u; return nil },
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	cfg, _ := config.Load(path)
	if !strings.Contains(shown, cfg.Users[0].TOTPSecret) || !strings.Contains(shown, "otpauth://") {
		t.Fatalf("QR %q does not carry the saved secret", shown)
	}
	if !strings.Contains(out.String(), "isn't right") {
		t.Errorf("wrong code was accepted:\n%s", out.String())
	}
}

// lazyReader answers the prompts: "n" to keeping the authenticator, a wrong
// code, then the right code for the secret printed so far.
type lazyReader struct {
	out  *bytes.Buffer
	now  time.Time
	step int
	buf  []byte
}

func (r *lazyReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		switch r.step {
		case 0:
			r.buf = []byte("n\n")
		case 1:
			r.buf = []byte("000000\n")
		default:
			s := r.out.String()
			i := strings.Index(s, "type: ")
			key := strings.ReplaceAll(strings.SplitN(s[i+6:], "\n", 2)[0], " ", "")
			code, _ := auth.TOTPCode(key, r.now)
			r.buf = []byte(code + "\n")
		}
		r.step++
	}
	n := copy(p, r.buf[:1]) // one byte at a time: never read ahead of a prompt
	r.buf = r.buf[n:]
	return n, nil
}

func TestNoAccounts(t *testing.T) {
	path := writeConfig(t)
	if err := Run(path, IO{In: bufio.NewReader(strings.NewReader("")), Out: &bytes.Buffer{}}); err != ErrNoAccounts {
		t.Fatalf("err = %v", err)
	}
	if err := Run(filepath.Join(t.TempDir(), "missing.toml"), IO{Out: &bytes.Buffer{}}); err == nil || !strings.Contains(err.Error(), "installed") {
		t.Fatalf("missing file: %v", err)
	}
}

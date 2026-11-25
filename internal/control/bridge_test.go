package control

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/coral-coder/windstream/internal/auth"
	"github.com/coral-coder/windstream/internal/config"
)

func bridgeController(t *testing.T) (*Controller, *PanelBridge) {
	t.Helper()
	c, err := New(Options{DataDir: t.TempDir(), Dev: true, Version: "9.9.9",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Logs: NewLogBuffer(10)})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.update(func(cfg *config.Config) error {
		cfg.Users = append(cfg.Users, config.User{Name: "Ararara", PasswordHash: hash, TOTPSecret: "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.panelAddr = "127.0.0.1:47333"
	p := newPanel(c)
	c.panel = p
	return c, newPanelBridge(p)
}

func TestBridgePasswordOnlySignIn(t *testing.T) {
	_, b := bridgeController(t)
	state := func() map[string]any {
		r := b.Call(BridgeRequest{Method: "GET", Path: "/api/state"})
		var m map[string]any
		if r.Status != 200 || json.Unmarshal([]byte(r.Body), &m) != nil {
			t.Fatalf("state: %+v", r)
		}
		return m
	}
	if state()["logged_in"] == true {
		t.Fatal("signed in before signing in")
	}
	if r := b.Call(BridgeRequest{Method: "POST", Path: "/api/login", Body: `{"username":"ararara","password":"wrong"}`}); r.Status != 401 {
		t.Fatalf("wrong password: %+v", r)
	}
	// No authenticator code: the account has one, but the control panel
	// on the computer itself asks only for the password.
	if r := b.Call(BridgeRequest{Method: "POST", Path: "/api/login", Body: `{"username":"ararara","password":"correct-horse-battery"}`}); r.Status != 200 {
		t.Fatalf("password sign-in: %+v", r)
	}
	if st := state(); st["logged_in"] != true || st["user"] != "Ararara" {
		t.Fatalf("not signed in: %v", st["logged_in"])
	}
	logs := b.Call(BridgeRequest{Method: "GET", Path: "/api/logs.zip"})
	data, err := base64.StdEncoding.DecodeString(logs.Body)
	if logs.Status != 200 || !logs.Base64 || err != nil || !strings.HasSuffix(logs.Filename, ".zip") {
		t.Fatalf("logs download: status %d base64 %v name %q", logs.Status, logs.Base64, logs.Filename)
	}
	if _, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("logs zip: %v", err)
	}
	if r := b.Call(BridgeRequest{Method: "POST", Path: "/api/logout", Body: `{}`}); r.Status != 200 {
		t.Fatalf("sign out: %+v", r)
	}
	if state()["logged_in"] == true {
		t.Fatal("still signed in after signing out")
	}
	for _, path := range []string{"/", "/admin.js", "/../windstream.toml", "http://evil/api/state"} {
		if r := b.Call(BridgeRequest{Method: "GET", Path: path}); r.Status != 404 {
			t.Errorf("%s: %d, want 404 (the bridge only reaches the API)", path, r.Status)
		}
	}
}

func TestBridgeServeOverPipe(t *testing.T) {
	_, b := bridgeController(t)
	in := strings.NewReader(`{"id":7,"method":"GET","path":"/api/state"}` + "\n")
	var out bytes.Buffer
	if err := b.Serve(in, &out); err != nil {
		t.Fatal(err)
	}
	var r BridgeResponse
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &r); err != nil || r.ID != 7 || r.Status != 200 {
		t.Fatalf("answer %q: %+v %v", out.String(), r, err)
	}
}

func TestPanelHTMLIsSelfContained(t *testing.T) {
	page, err := PanelHTML()
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{`src="/`, `href="/`} {
		if strings.Contains(page, ref) {
			t.Errorf("page still refers to a server file (%s…)", ref)
		}
	}
	for _, want := range []string{"<style>", "windstreamCall", "data:image/png;base64,"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, "Authenticator code <input name=\"code\" inputmode=\"numeric\" autocomplete=\"one-time-code\" maxlength=\"6\"></label>\n      <button type=\"submit\">Sign in") {
		t.Error("the sign-in form still asks for an authenticator code")
	}
}

//go:build windows

package winapp

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"

	"github.com/coral-coder/windstream/internal/control"
)

// Opens the real window (WebView2) with the bundled control panel and a
// stand-in server, and checks the page loads, asks for the state and
// shows the right screen. CI sets WINDSTREAM_TEST_WINDOW (it opens a
// window on the desktop).
func TestPanelWindowShowsControlPanel(t *testing.T) {
	if os.Getenv("WINDSTREAM_TEST_WINDOW") == "" {
		t.Skip("set WINDSTREAM_TEST_WINDOW=1 (opens a window)")
	}
	v, err := webviewloader.GetInstalledVersion()
	if err != nil {
		t.Skipf("no WebView2 runtime here: %v", err)
	}
	t.Logf("WebView2 %s", v)
	page, err := control.PanelHTML()
	if err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Not t.TempDir: WebView2's helper processes release their files a
	// little after the window closes.
	dataDir, err := os.MkdirTemp("", "ws-webview-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dataDir)
	w := webview2.NewWithOptions(webview2.WebViewOptions{DataPath: dataDir,
		WindowOptions: webview2.WindowOptions{Title: "Windstream test", Width: 900, Height: 700}})
	if w == nil {
		t.Fatal("window not created")
	}
	defer w.Destroy()
	var mu sync.Mutex
	var calls []string
	signedIn := false
	if err := w.Bind("windstreamCall", func(method, path, body string) (control.BridgeResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, method+" "+path)
		switch path {
		case "/api/login":
			if !strings.Contains(body, `"password":"pw"`) || strings.Contains(body, `"code"`) {
				return control.BridgeResponse{Status: 401, Body: `{"error":"bad"}`}, nil
			}
			signedIn = true
			return control.BridgeResponse{Status: 200, Body: `{"ok":true}`}, nil
		case "/api/state":
			if signedIn {
				return control.BridgeResponse{Status: 200, Body: `{"version":"9.9.9","setup_needed":false,"logged_in":true,"user":"tester"}`}, nil
			}
			return control.BridgeResponse{Status: 200, Body: `{"version":"9.9.9","setup_needed":false,"logged_in":false}`}, nil
		}
		return control.BridgeResponse{Status: 200, Body: `{}`}, nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = w.Bind("windstreamOpen", func(string) {})
	var views []string
	_ = w.Bind("reportView", func(view string) {
		mu.Lock()
		defer mu.Unlock()
		if len(views) == 0 || views[len(views)-1] != view {
			views = append(views, view)
		}
		if view == "dash" {
			w.Terminate()
		}
	})
	// Sign in with only a password once the login screen shows, then wait
	// for the control panel.
	w.Init(`setInterval(() => {
		for (const v of ['setup', 'login', 'dash']) {
			const e = document.getElementById('view-' + v);
			if (e && !e.classList.contains('hidden')) window.reportView(v);
		}
		const f = document.getElementById('form-login');
		if (f && !document.getElementById('view-login').classList.contains('hidden') && !f.dataset.sent) {
			f.dataset.sent = '1';
			f.username.value = 'tester'; f.password.value = 'pw';
			f.requestSubmit();
		}
	}, 200);`)
	go func() {
		time.Sleep(60 * time.Second)
		w.Dispatch(w.Terminate)
	}()
	w.SetHtml(page)
	w.Run()
	mu.Lock()
	defer mu.Unlock()
	t.Logf("screens %v, calls %v", views, calls)
	if len(views) < 2 || views[0] != "login" || views[len(views)-1] != "dash" {
		t.Fatalf("expected the sign-in screen, then the control panel; saw %v", views)
	}
}

// Runs the built exe as the window process (`--panel`) over its pipe, like
// the server does: it must ask for the state, and close when the server
// goes away. CI sets WINDSTREAM_TEST_EXE after building.
func TestPanelProcessOverPipe(t *testing.T) {
	exe := os.Getenv("WINDSTREAM_TEST_EXE")
	if exe == "" {
		t.Skip("set WINDSTREAM_TEST_EXE to a built Windstream.exe")
	}
	if _, err := webviewloader.GetInstalledVersion(); err != nil {
		t.Skipf("no WebView2 runtime here: %v", err)
	}
	cmd := exec.Command(exe, panelFlag)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	asked := make(chan string, 8)
	go func() {
		dec := json.NewDecoder(stdout)
		enc := json.NewEncoder(stdin)
		for {
			var r control.BridgeRequest
			if dec.Decode(&r) != nil {
				return
			}
			asked <- r.Method + " " + r.Path
			_ = enc.Encode(control.BridgeResponse{ID: r.ID, Status: 200, ContentType: "application/json",
				Body: `{"version":"9.9.9","setup_needed":false,"logged_in":false}`})
		}
	}()
	select {
	case got := <-asked:
		if got != "GET /api/state" {
			t.Errorf("first request %q", got)
		}
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("the window never asked for the state; stderr: %s", stderr.String())
	}
	_ = stdin.Close() // the server stops
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("window process: %v; stderr: %s", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the window stayed open after the server went away")
	}
}

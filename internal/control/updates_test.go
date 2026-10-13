package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/coral-coder/windstream/internal/config"
	"github.com/coral-coder/windstream/internal/update"
)

type fakeUpdater struct {
	fakeNoPlatform
	unattended bool
	installed  string
}

type fakeNoPlatform struct{}

func (fakeNoPlatform) SteamExe() string        { return "" }
func (fakeNoPlatform) Uninstall(bool) error    { return nil }
func (fakeNoPlatform) Quit()                   {}
func (fakeNoPlatform) PanelReady(string)       {}
func (f *fakeUpdater) UpdateAsset() string     { return "Windstream.exe" }
func (f *fakeUpdater) UnattendedUpdates() bool { return f.unattended }
func (f *fakeUpdater) InstallUpdate(file, version string) error {
	b, err := os.ReadFile(file)
	f.installed = version + ":" + string(b)
	return err
}

func fakeRelease(t *testing.T, tag string, exe []byte) {
	h := sha256.Sum256(exe)
	sums := hex.EncodeToString(h[:]) + "  Windstream.exe\n"
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(update.UseServer(srv.URL))
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"x","assets":[
			{"name":"Windstream.exe","browser_download_url":"%[2]s/download/%[1]s/Windstream.exe","size":%[3]d},
			{"name":"SHA256SUMS.txt","browser_download_url":"%[2]s/download/%[1]s/SHA256SUMS.txt","size":%[4]d}]}`,
			tag, srv.URL, len(exe), len(sums))
	})
	mux.HandleFunc("/download/"+tag+"/Windstream.exe", func(w http.ResponseWriter, r *http.Request) { w.Write(exe) })
	mux.HandleFunc("/download/"+tag+"/SHA256SUMS.txt", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, sums) })
}

func newUpdateController(t *testing.T, version string, p Platform) *Controller {
	c, err := New(Options{DataDir: t.TempDir(), Version: version, Platform: p,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Logs: NewLogBuffer(10)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAutomaticUpdate(t *testing.T) {
	fakeRelease(t, "v1.0.16", []byte("new build"))
	p := &fakeUpdater{unattended: true}
	c := newUpdateController(t, "1.0.15", p)
	ctx := context.Background()
	c.checkForUpdate(ctx)
	st := c.updateStatus()
	if !st.Supported || !st.Available || st.Latest != "1.0.16" || !st.Auto || !st.Unattended {
		t.Fatalf("status %+v", st)
	}
	if !c.autoInstallDue(p) {
		t.Fatal("update not due with nobody streaming")
	}
	if err := c.installUpdate(ctx, p); err != nil {
		t.Fatal(err)
	}
	if p.installed != "1.0.16:new build" {
		t.Errorf("installed %q", p.installed)
	}

	// Switched off: offered, not installed.
	_ = c.update(func(cf *config.Config) error { cf.Updates.Auto = false; return nil })
	if c.autoInstallDue(p) {
		t.Error("installs although automatic updates are off")
	}
}

func TestMacUpdatesNeedSomeoneThere(t *testing.T) {
	fakeRelease(t, "v1.0.16", []byte("new build"))
	p := &fakeUpdater{unattended: false}
	c := newUpdateController(t, "1.0.15", p)
	c.checkForUpdate(context.Background())
	if !c.updateStatus().Available || c.autoInstallDue(p) {
		t.Fatalf("status %+v, due %v: must be offered but not installed by itself", c.updateStatus(), c.autoInstallDue(p))
	}
}

func TestNoUpdateWhenCurrentOrDev(t *testing.T) {
	fakeRelease(t, "v1.0.15", []byte("same build"))
	p := &fakeUpdater{unattended: true}
	c := newUpdateController(t, "1.0.15", p)
	c.checkForUpdate(context.Background())
	if c.updateStatus().Available || c.autoInstallDue(p) {
		t.Error("the running version offered as an update")
	}
	if _, ok := newUpdateController(t, "dev", p).updatePlatform(); ok {
		t.Error("development builds must not update")
	}
}

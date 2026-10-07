package control

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// FolderOpener is implemented by platforms that can show a folder to the
// user (Explorer on Windows).
type FolderOpener interface {
	OpenFolder(path string) error
}

// SystemInfoer is implemented by platforms that can describe the PC (OS
// version and the like) for diagnostics.
type SystemInfoer interface {
	SystemInfo() string
}

// logFiles are the files a log bundle carries, newest first.
var logFiles = []string{"windstream.log", "windstream.log.1", "crash.log", "crash.log.1"}

// maxBundledLog caps each file in the bundle (the tail is kept).
const maxBundledLog = 10 << 20

func (c *Controller) logsDir() string { return filepath.Join(c.opts.DataDir, "logs") }

// handleLogsZip downloads the logs plus a diagnostics summary as one zip
// file. It never includes passwords, authenticator secrets or session keys.
func (p *panel) handleLogsZip(w http.ResponseWriter, r *http.Request) {
	name := "windstream-logs-" + time.Now().Format("2006-01-02-1504") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	defer zw.Close()

	if f, err := zw.CreateHeader(&zip.FileHeader{Name: "diagnostics.txt", Method: zip.Deflate, Modified: time.Now()}); err == nil {
		_, _ = io.WriteString(f, p.c.diagnostics())
	}
	for _, fn := range logFiles {
		path := filepath.Join(p.c.logsDir(), fn)
		src, err := os.Open(path)
		if err != nil {
			continue
		}
		hdr := &zip.FileHeader{Name: fn, Method: zip.Deflate, Modified: time.Now()}
		if st, err := src.Stat(); err == nil {
			hdr.Modified = st.ModTime()
			if st.Size() > maxBundledLog {
				_, _ = src.Seek(st.Size()-maxBundledLog, io.SeekStart)
			}
		}
		if dst, err := zw.CreateHeader(hdr); err == nil {
			_, _ = io.Copy(dst, src)
		}
		src.Close()
	}
}

// handleOpenLogs shows the logs folder in Explorer.
func (p *panel) handleOpenLogs(w http.ResponseWriter, r *http.Request) {
	fo, ok := p.c.opts.Platform.(FolderOpener)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "Opening folders is only available in the Windows app. The logs are in "+p.c.logsDir())
		return
	}
	if err := fo.OpenFolder(p.c.logsDir()); err != nil {
		jsonErr(w, http.StatusInternalServerError, "Could not open the folder: "+err.Error())
		return
	}
	jsonOK(w, map[string]any{"ok": true, "path": p.c.logsDir()})
}

// diagnostics summarizes the app's state for a bug report.
func (c *Controller) diagnostics() string {
	var b strings.Builder
	line := func(k string, v any) { fmt.Fprintf(&b, "%-18s %v\n", k+":", v) }
	line("Windstream", c.opts.Version)
	line("Generated", time.Now().Format(time.RFC1123Z))
	line("Platform", runtime.GOOS+"/"+runtime.GOARCH)
	if si, ok := c.opts.Platform.(SystemInfoer); ok {
		line("System", si.SystemInfo())
	}
	if c.opts.LastCrash != "" {
		line("Previous crash", c.opts.LastCrash)
	}
	line("Logs folder", c.logsDir())

	cfg := c.Config()
	b.WriteString("\n== Settings ==\n")
	if s, err := json.MarshalIndent(settingsFrom(cfg), "", "  "); err == nil {
		b.Write(s)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "accounts: %d\n", len(cfg.Users))

	b.WriteString("\n== Streaming engine ==\n")
	if s, err := json.MarshalIndent(c.stackStatus(), "", "  "); err == nil {
		b.Write(s)
		b.WriteString("\n")
	}
	if e := c.httpsError(); e != "" {
		line("HTTPS error", e)
	}

	b.WriteString("\n== Components ==\n")
	for _, d := range c.deps.Snapshot() {
		if s, err := json.Marshal(d); err == nil {
			b.Write(s)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n== Network ==\n")
	if s, err := json.MarshalIndent(c.netStatus(), "", "  "); err == nil {
		b.Write(s)
		b.WriteString("\n")
	}

	b.WriteString("\n== Displays ==\n")
	for _, o := range listOutputs() {
		fmt.Fprintf(&b, "%d: %s %dx%d at %d,%d primary=%v monitor=%q gpu=%q (adapter %d output %d)\n",
			o.Index, o.DeviceName, o.Width, o.Height, o.X, o.Y, o.Primary, o.MonitorName, o.AdapterName, o.Adapter, o.Output)
	}
	return b.String()
}

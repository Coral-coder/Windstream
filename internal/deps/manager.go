package deps

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// State of a component.
type State string

const (
	StatePending     State = "pending"
	StateDownloading State = "downloading"
	StateInstalling  State = "installing"
	StateReady       State = "ready"
	StateError       State = "error"
	StateSkipped     State = "skipped"
)

// Component is a dependency's live status (shown on the dashboard).
type Component struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Purpose  string  `json:"purpose"`
	State    State   `json:"state"`
	Progress float64 `json:"progress"` // 0..1 while downloading
	Detail   string  `json:"detail,omitempty"`
}

// Manager ensures every dependency is present.
type Manager struct {
	dataDir string
	log     *slog.Logger

	mu    sync.Mutex
	comps map[string]*Component
	order []string

	ffmpegPath string
	// VDDMode is the mode written into the driver settings before install.
	VDDWidth, VDDHeight, VDDRefresh int
}

// NewManager creates a manager storing downloads under dataDir.
func NewManager(dataDir string, log *slog.Logger) *Manager {
	m := &Manager{dataDir: dataDir, log: log, comps: map[string]*Component{}}
	m.add("ffmpeg", "Video & audio encoder", "Captures the screen and encodes H.264/Opus")
	m.add("vdd", "Virtual display", "Gives the PC a virtual monitor to stream, no screen needed")
	m.add("vigem", "Controller driver", "Lets remote controllers appear as Xbox controllers")
	return m
}

func (m *Manager) add(id, name, purpose string) {
	m.comps[id] = &Component{ID: id, Name: name, Purpose: purpose, State: StatePending}
	m.order = append(m.order, id)
}

func (m *Manager) set(id string, st State, progress float64, detail string) {
	m.mu.Lock()
	c := m.comps[id]
	c.State, c.Progress, c.Detail = st, progress, detail
	m.mu.Unlock()
}

// Snapshot returns the components in display order.
func (m *Manager) Snapshot() []Component {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Component, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, *m.comps[id])
	}
	return out
}

// Ready reports whether a component is usable.
func (m *Manager) Ready(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.comps[id].State == StateReady
}

// FFmpegPath returns the ffmpeg to use.
func (m *Manager) FFmpegPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ffmpegPath != "" {
		return m.ffmpegPath
	}
	return "ffmpeg"
}

// EnsureAll installs whatever is missing. Each component is independent: a
// failure is recorded and the rest continue.
func (m *Manager) EnsureAll(ctx context.Context) {
	m.ensureFFmpeg(ctx)
	if runtime.GOOS != "windows" {
		m.set("vdd", StateSkipped, 0, "Windows only")
		m.set("vigem", StateSkipped, 0, "Windows only")
		return
	}
	m.run(ctx, "vigem", m.ensureViGEm)
	m.run(ctx, "vdd", m.ensureVDD)
}

func (m *Manager) run(ctx context.Context, id string, fn func(context.Context) error) {
	err := guard(func() error { return fn(ctx) })
	if err != nil {
		m.log.Error("dependency install failed", "component", id, "error", err)
		detail := err.Error()
		if i := len(detail); i > 200 { // keep the dashboard message short
			detail = detail[:200]
		}
		m.set(id, StateError, 0, detail)
		return
	}
	m.set(id, StateReady, 1, "")
}

func (m *Manager) progressFn(id string) Progress {
	return func(done, total int64) {
		p := 0.0
		if total > 0 {
			p = float64(done) / float64(total)
		}
		m.set(id, StateDownloading, p, fmt.Sprintf("%.0f / %.0f MB", float64(done)/1e6, float64(total)/1e6))
	}
}

func (m *Manager) ensureFFmpeg(ctx context.Context) {
	if runtime.GOOS != "windows" {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			m.mu.Lock()
			m.ffmpegPath = p
			m.mu.Unlock()
			m.set("ffmpeg", StateReady, 1, p)
			return
		}
		m.set("ffmpeg", StateError, 0, "install ffmpeg on this system")
		return
	}
	binDir := filepath.Join(m.dataDir, "bin")
	exe := filepath.Join(binDir, "ffmpeg.exe")
	marker := filepath.Join(binDir, "ffmpeg.version")
	if b, err := os.ReadFile(marker); err == nil && string(b) == FFmpeg.SHA256 {
		if _, err := os.Stat(exe); err == nil {
			m.mu.Lock()
			m.ffmpegPath = exe
			m.mu.Unlock()
			m.set("ffmpeg", StateReady, 1, FFmpeg.Name)
			return
		}
	}
	m.run(ctx, "ffmpeg", func(ctx context.Context) error {
		zipPath := filepath.Join(m.dataDir, "downloads", filepath.Base(FFmpeg.URL))
		// Nothing streams without the encoder, so a failed download (PC
		// still connecting to Wi-Fi at logon, a flaky connection) is retried
		// until it works rather than left for the user to notice.
		for wait := 10 * time.Second; ; wait = min(wait*2, 5*time.Minute) {
			m.log.Info("downloading", "artifact", FFmpeg.Name)
			err := Download(ctx, FFmpeg, zipPath, m.progressFn("ffmpeg"))
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				return err
			}
			m.log.Warn("ffmpeg download failed; retrying", "error", err, "in", wait)
			m.set("ffmpeg", StateError, 0, fmt.Sprintf("download failed (%s); retrying in %s", shorten(err.Error(), 120), wait))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		m.set("ffmpeg", StateInstalling, 1, "extracting")
		if _, err := ExtractMatching(zipPath, "/bin/ffmpeg.exe", binDir); err != nil {
			return err
		}
		if err := os.WriteFile(marker, []byte(FFmpeg.SHA256), 0o644); err != nil {
			return err
		}
		_ = os.Remove(zipPath) // 115 MB we no longer need
		m.mu.Lock()
		m.ffmpegPath = exe
		m.mu.Unlock()
		m.log.Info("installed", "artifact", FFmpeg.Name, "path", exe)
		return nil
	})
}

func shorten(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

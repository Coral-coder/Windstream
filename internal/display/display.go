// Package display creates (virtual mode) or selects (monitor mode) the
// Windows display that is streamed, and tells the capture pipeline where to
// find it.
package display

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/coral-coder/windstream/internal/config"
)

// ErrUnsupported is returned for virtual/monitor modes on non-Windows hosts.
var ErrUnsupported = errors.New("display: virtual and monitor modes require Windows; use mode = \"test\"")

// Output is one DXGI output (a monitor attached to the desktop).
type Output struct {
	// Index is the flat index across all adapters, as `windstream displays`
	// prints it and as display.monitor accepts it.
	Index       int
	Adapter     int    // DXGI adapter index (ffmpeg -init_hw_device d3d11va=N)
	AdapterName string // e.g. "NVIDIA GeForce RTX 4070"
	Output      int    // output index on that adapter (ddagrab output_idx)
	DeviceName  string // GDI name, e.g. \\.\DISPLAY2
	MonitorName string // e.g. the IDD or monitor model, from EnumDisplayDevices
	Width       int
	Height      int
	X, Y        int
	Primary     bool
}

// Target is what the capture pipeline needs.
type Target struct {
	Test    bool // synthetic source, no capture
	Adapter int
	Output  int
	Width   int
	Height  int
	Name    string
}

// Manager owns the streamed display for the lifetime of the server.
type Manager struct {
	cfg config.Display
	log *slog.Logger

	mu    sync.Mutex
	state platformState
}

// New creates a manager.
func New(cfg config.Display, log *slog.Logger) *Manager { return &Manager{cfg: cfg, log: log} }

// Start prepares the display and launches the configured application.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.Mode != "test" {
		if err := m.startPlatform(ctx); err != nil {
			return err
		}
	} else {
		m.log.Info("display: test pattern mode (no capture)")
	}
	if len(m.cfg.Launch) > 0 {
		cmd := exec.Command(m.cfg.Launch[0], m.cfg.Launch[1:]...)
		configureLaunch(cmd)
		if err := cmd.Start(); err != nil {
			m.log.Warn("launch failed", "command", strings.Join(m.cfg.Launch, " "), "error", err)
		} else {
			m.log.Info("launched", "command", strings.Join(m.cfg.Launch, " "), "pid", cmd.Process.Pid)
			go func() { _ = cmd.Wait() }()
		}
	}
	return nil
}

// Target resolves the capture target. It re-enumerates every time because
// DXGI indices change when monitors are added, removed or rearranged.
func (m *Manager) Target() (Target, error) {
	if m.cfg.Mode == "test" {
		return Target{Test: true, Width: m.cfg.Width, Height: m.cfg.Height, Name: "test pattern"}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.targetPlatform()
}

// RecoverUnclean undoes display changes (virtual monitor left on, primary
// display moved) recorded by a previous run that ended without cleaning up,
// e.g. a crash mid-stream. Safe to call when nothing needs undoing.
func (m *Manager) RecoverUnclean() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.Mode != "test" {
		m.recoverPlatform()
	}
}

// Stop restores the desktop layout and turns the virtual monitor off.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.Mode != "test" {
		m.stopPlatform()
	}
}

// SelectOutput picks an output by flat index or GDI device name.
func SelectOutput(outputs []Output, sel string) (Output, error) {
	sel = strings.TrimSpace(sel)
	if i, err := strconv.Atoi(sel); err == nil {
		for _, o := range outputs {
			if o.Index == i {
				return o, nil
			}
		}
		return Output{}, fmt.Errorf("display: no monitor with index %d (run `windstream displays`)", i)
	}
	for _, o := range outputs {
		if strings.EqualFold(o.DeviceName, sel) {
			return o, nil
		}
	}
	return Output{}, fmt.Errorf("display: no monitor named %q (run `windstream displays`)", sel)
}

// FindByMonitorName returns the first output whose monitor/adapter description
// contains substr (case-insensitive).
func FindByMonitorName(outputs []Output, substr string) (Output, bool) {
	substr = strings.ToLower(substr)
	for _, o := range outputs {
		if strings.Contains(strings.ToLower(o.MonitorName), substr) {
			return o, true
		}
	}
	return Output{}, false
}

// Translate returns positions that keep every display's relative layout but
// put newPrimary at the origin. Windows requires the primary at (0,0).
func Translate(outputs []Output, newPrimary string) map[string][2]int {
	var ox, oy int
	for _, o := range outputs {
		if strings.EqualFold(o.DeviceName, newPrimary) {
			ox, oy = o.X, o.Y
		}
	}
	pos := make(map[string][2]int, len(outputs))
	for _, o := range outputs {
		pos[o.DeviceName] = [2]int{o.X - ox, o.Y - oy}
	}
	return pos
}

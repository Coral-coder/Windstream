//go:build windows

package display

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

type platformState struct {
	vdd            *vddDevice
	weEnabledVDD   bool
	device         string // GDI name of the streamed display
	origPrimary    string
	changedPrimary bool
}

// savedState survives a hard kill (Task Scheduler "End" terminates the
// process without cleanup) so the next run can still undo our changes.
type savedState struct {
	WeEnabledVDD bool   `json:"we_enabled_vdd"`
	OrigPrimary  string `json:"orig_primary"`
}

func statePath() string {
	dir := os.Getenv("ProgramData")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "Windstream", "display-state.json")
}

func loadState() savedState {
	var st savedState
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func (m *Manager) saveState() {
	st := savedState{WeEnabledVDD: m.state.weEnabledVDD}
	if m.state.changedPrimary {
		st.OrigPrimary = m.state.origPrimary
	}
	if !st.WeEnabledVDD && st.OrigPrimary == "" {
		_ = os.Remove(statePath())
		return
	}
	if b, err := json.Marshal(st); err == nil {
		_ = os.MkdirAll(filepath.Dir(statePath()), 0o700)
		_ = os.WriteFile(statePath(), b, 0o600)
	}
}

func (m *Manager) startPlatform(ctx context.Context) error {
	prev := loadState()
	if prev.WeEnabledVDD || prev.OrigPrimary != "" {
		m.log.Info("recovering display state from an unclean shutdown", "orig_primary", prev.OrigPrimary)
		m.state.weEnabledVDD = prev.WeEnabledVDD
		m.state.origPrimary = prev.OrigPrimary
		m.state.changedPrimary = prev.OrigPrimary != ""
	}
	switch m.cfg.Mode {
	case "virtual":
		return m.startVirtual(ctx)
	case "monitor":
		outs, err := List()
		if err != nil {
			return err
		}
		o, err := SelectOutput(outs, m.cfg.Monitor)
		if err != nil {
			return err
		}
		m.state.device = o.DeviceName
		if m.cfg.SetMode {
			if err := setMode(o.DeviceName, m.cfg.Width, m.cfg.Height, m.cfg.Refresh, o.X, o.Y); err != nil {
				return fmt.Errorf("set %dx%d@%d on %s: %w", m.cfg.Width, m.cfg.Height, m.cfg.Refresh, o.DeviceName, err)
			}
		}
		m.log.Info("streaming existing monitor", "device", o.DeviceName, "monitor", o.MonitorName, "gpu", o.AdapterName)
		return m.maybeMakePrimary()
	}
	return fmt.Errorf("display: unknown mode %q", m.cfg.Mode)
}

func (m *Manager) startVirtual(ctx context.Context) error {
	vdd, err := findVDD(m.cfg.VirtualDevice)
	if err != nil {
		return err
	}
	m.state.vdd = vdd
	if !vdd.enabled {
		m.log.Info("enabling virtual display adapter", "device", vdd.name)
		if err := vdd.setEnabled(true); err != nil {
			return err
		}
		m.state.weEnabledVDD = true
		m.saveState()
	}

	// Wait for Windows to expose the virtual monitor.
	deadline := time.Now().Add(m.cfg.StartupTimeout.Duration)
	var dev gdiDisplay
	for {
		found := false
		for _, d := range enumGDIDisplays() {
			if strings.Contains(strings.ToLower(d.description), strings.ToLower(m.cfg.VirtualDevice)) {
				dev, found = d, true
				if d.attached {
					break
				}
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("virtual monitor did not appear within %s", m.cfg.StartupTimeout.Duration)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	m.state.device = dev.name

	// Apply the resolution. Placing it right of the existing desktop also
	// attaches it if Windows left it detached.
	x := 0
	if outs, err := List(); err == nil {
		for _, o := range outs {
			if o.DeviceName != dev.name && o.X+o.Width > x {
				x = o.X + o.Width
			}
		}
	}
	if err := setMode(dev.name, m.cfg.Width, m.cfg.Height, m.cfg.Refresh, x, 0); err != nil {
		return fmt.Errorf("set %dx%d@%d on virtual monitor %s: %w (add this mode to the driver's settings)",
			m.cfg.Width, m.cfg.Height, m.cfg.Refresh, dev.name, err)
	}
	m.log.Info("virtual monitor ready", "device", dev.name, "mode", fmt.Sprintf("%dx%d@%d", m.cfg.Width, m.cfg.Height, m.cfg.Refresh))
	return m.maybeMakePrimary()
}

func (m *Manager) maybeMakePrimary() error {
	if !m.cfg.MakePrimary {
		return nil
	}
	outs, err := List()
	if err != nil {
		return err
	}
	current := ""
	for _, o := range outs {
		if o.Primary {
			current = o.DeviceName
		}
	}
	if current == m.state.device {
		return nil // already primary (possibly from a previous run we are recovering)
	}
	if !m.state.changedPrimary {
		m.state.origPrimary = current
	}
	if err := makePrimary(m.state.device, outs); err != nil {
		m.log.Warn("could not make the streamed display primary; games may open on another monitor", "error", err)
		return nil
	}
	m.state.changedPrimary = true
	m.saveState()
	m.log.Info("streamed display is now primary", "device", m.state.device, "previous", m.state.origPrimary)
	return nil
}

func (m *Manager) targetPlatform() (Target, error) {
	outs, err := List()
	if err != nil {
		return Target{}, err
	}
	for _, o := range outs {
		if strings.EqualFold(o.DeviceName, m.state.device) {
			return Target{Adapter: o.Adapter, Output: o.Output, Width: o.Width, Height: o.Height,
				Name: fmt.Sprintf("%s (%s on %s)", o.DeviceName, o.MonitorName, o.AdapterName)}, nil
		}
	}
	return Target{}, fmt.Errorf("display %s is no longer attached to the desktop", m.state.device)
}

func (m *Manager) stopPlatform() {
	if m.state.changedPrimary && m.state.origPrimary != "" {
		if outs, err := List(); err == nil {
			if err := makePrimary(m.state.origPrimary, outs); err != nil {
				m.log.Warn("restoring primary display failed", "error", err)
			} else {
				m.log.Info("primary display restored", "device", m.state.origPrimary)
			}
		}
		m.state.changedPrimary = false
	}
	if m.state.vdd != nil {
		if m.state.weEnabledVDD && !m.cfg.VirtualKeepEnabled {
			if err := m.state.vdd.setEnabled(false); err != nil {
				m.log.Warn("disabling virtual display adapter failed", "error", err)
			} else {
				m.log.Info("virtual display adapter disabled")
			}
		}
		m.state.vdd.close()
		m.state.vdd = nil
	}
	m.state.weEnabledVDD = false
	m.saveState()
}

func configureLaunch(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

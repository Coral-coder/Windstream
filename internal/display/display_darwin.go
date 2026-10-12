//go:build darwin

package display

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/coral-coder/windstream/internal/macos"
)

// On a Mac Windstream streams an existing display (there is no virtual
// display driver); nothing about the desktop layout is changed.
type platformState struct{}

var errNoVirtual = errors.New("display: a virtual screen is not available on macOS; streaming an existing display")

func (m *Manager) startPlatform(context.Context) error {
	if m.cfg.Mode == "virtual" {
		return errNoVirtual
	}
	return nil
}

func (m *Manager) targetPlatform() (Target, error) {
	outs, err := List()
	if err != nil {
		return Target{}, err
	}
	if len(outs) == 0 {
		return Target{}, errors.New("no active display (is the Mac asleep or the lid closed without an external screen?)")
	}
	pick := outs[0]
	for _, o := range outs {
		if o.Primary {
			pick = o
		}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(m.cfg.Monitor)); err == nil && n >= 0 && n < len(outs) {
		pick = outs[n]
	}
	return Target{Adapter: 0, Output: pick.Output, Width: pick.Width, Height: pick.Height, Name: pick.MonitorName}, nil
}

func (m *Manager) stopPlatform()    {}
func (m *Manager) recoverPlatform() {}

// launchCommand opens links (steam://...) with the default app.
func launchCommand(launch []string) *exec.Cmd {
	for _, a := range launch {
		if strings.Contains(a, "://") {
			return exec.Command("/usr/bin/open", a)
		}
	}
	return exec.Command(launch[0], launch[1:]...)
}

// List enumerates the active displays. Sizes and positions are in points
// (what mouse events use); Output is the AVFoundation screen number.
func List() ([]Output, error) {
	ds, err := macos.Displays()
	if err != nil {
		return nil, err
	}
	outs := make([]Output, 0, len(ds))
	for i, d := range ds {
		name := "Display"
		if d.Builtin {
			name = "Built-in display"
		}
		outs = append(outs, Output{
			Index: i, Adapter: 0, Output: i,
			DeviceName:  fmt.Sprintf("display %d", d.ID),
			MonitorName: fmt.Sprintf("%s (%dx%d pixels)", name, d.PixelsWide, d.PixelsHigh),
			Width:       int(d.Bounds.W), Height: int(d.Bounds.H),
			X: int(d.Bounds.Origin.X), Y: int(d.Bounds.Origin.Y),
			Primary: d.Main,
		})
	}
	return outs, nil
}

// VirtualDeviceStatus: there is no virtual display driver on macOS.
func VirtualDeviceStatus(string) (string, error) { return "", ErrUnsupported }

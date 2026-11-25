//go:build !windows && !darwin

package display

import (
	"context"
	"os/exec"
)

type platformState struct{}

func (m *Manager) startPlatform(context.Context) error { return ErrUnsupported }
func (m *Manager) targetPlatform() (Target, error)     { return Target{}, ErrUnsupported }
func (m *Manager) stopPlatform()                       {}

// fitPlatform: changing the resolution is only done on Windows.
func (m *Manager) fitPlatform(maxW, maxH int) (Mode, error) { return Mode{}, nil }
func (m *Manager) recoverPlatform()                         {}

func launchCommand(launch []string) *exec.Cmd { return exec.Command(launch[0], launch[1:]...) }

// List enumerates outputs (Windows only).
func List() ([]Output, error) { return nil, ErrUnsupported }

// VirtualDeviceStatus describes the virtual display adapter (Windows only).
func VirtualDeviceStatus(string) (string, error) { return "", ErrUnsupported }

//go:build !windows

package display

import (
	"context"
	"os/exec"
)

type platformState struct{}

func (m *Manager) startPlatform(context.Context) error { return ErrUnsupported }
func (m *Manager) targetPlatform() (Target, error)     { return Target{}, ErrUnsupported }
func (m *Manager) stopPlatform()                       {}
func (m *Manager) recoverPlatform()                    {}

func configureLaunch(*exec.Cmd) {}

// List enumerates outputs (Windows only).
func List() ([]Output, error) { return nil, ErrUnsupported }

// VirtualDeviceStatus describes the virtual display adapter (Windows only).
func VirtualDeviceStatus(string) (string, error) { return "", ErrUnsupported }

//go:build !windows

package deps

import (
	"context"
	"errors"
)

var errWindowsOnly = errors.New("Windows only")

func (m *Manager) ensureViGEm(context.Context) error { return errWindowsOnly }
func (m *Manager) ensureVDD(context.Context) error   { return errWindowsOnly }

// UninstallVDD removes the virtual display device (Windows only).
func UninstallVDD() error { return errWindowsOnly }

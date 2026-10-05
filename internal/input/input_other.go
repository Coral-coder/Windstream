//go:build !windows

package input

import "log/slog"

func newPlatform(Options, *slog.Logger) (Injector, error) { return Noop{}, ErrUnsupported }

func platformCheck(Options) Probe {
	return Probe{KeyboardMouse: ErrUnsupported, Gamepads: ErrUnsupported}
}

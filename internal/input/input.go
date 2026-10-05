// Package input injects client input into the Windows host: keyboard and
// mouse through SendInput, gamepads as virtual Xbox 360 controllers through
// the ViGEmBus driver.
package input

import (
	"errors"
	"log/slog"

	"github.com/coral-coder/windstream/internal/protocol"
)

// ErrUnsupported is returned when the platform has no injector backend.
var ErrUnsupported = errors.New("input: input injection is only supported on Windows")

// Options controls which device classes a client may drive.
type Options struct {
	Gamepads    bool
	Keyboard    bool
	Mouse       bool
	MaxGamepads int
}

// Injector drives input for one client.
type Injector interface {
	GamepadConnect(idx int, name string) error
	GamepadDisconnect(idx int) error
	GamepadState(idx int, st protocol.GamepadState) error
	Key(code string, down bool) error
	MouseMove(dx, dy int32) error
	MouseButton(button uint8, down bool) error
	MouseWheel(dx, dy int32) error
	// Close releases every key, button and virtual controller this client
	// still holds, so a dropped connection never leaves input stuck.
	Close() error
}

// Noop discards all input.
type Noop struct{}

func (Noop) GamepadConnect(int, string) error              { return nil }
func (Noop) GamepadDisconnect(int) error                   { return nil }
func (Noop) GamepadState(int, protocol.GamepadState) error { return nil }
func (Noop) Key(string, bool) error                        { return nil }
func (Noop) MouseMove(int32, int32) error                  { return nil }
func (Noop) MouseButton(uint8, bool) error                 { return nil }
func (Noop) MouseWheel(int32, int32) error                 { return nil }
func (Noop) Close() error                                  { return nil }

// New returns the platform injector. If gamepads were requested but ViGEm is
// unavailable, keyboard and mouse still work and the error explains why
// controllers will not.
func New(opts Options, log *slog.Logger) (Injector, error) {
	return newPlatform(opts, log)
}

// Probe reports per-class availability for `windstream check`.
type Probe struct {
	KeyboardMouse error
	Gamepads      error
}

// Check probes the platform backends without creating devices that persist.
func Check(opts Options) Probe { return platformCheck(opts) }

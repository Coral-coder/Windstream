//go:build windows

package input

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/coral-coder/windstream/internal/protocol"
)

type winInjector struct {
	opts Options
	log  *slog.Logger

	mu      sync.Mutex
	closed  bool
	vigem   *vigemClient
	padErr  error
	pads    map[int]*x360Pad
	keys    map[string]bool // held keys, released on Close
	buttons map[uint8]bool  // held mouse buttons, released on Close
}

func newPlatform(opts Options, log *slog.Logger) (Injector, error) {
	inj := &winInjector{opts: opts, log: log, pads: map[int]*x360Pad{}, keys: map[string]bool{}, buttons: map[uint8]bool{}}
	if opts.Gamepads {
		v, err := newViGEmClient()
		if err != nil {
			inj.padErr = err
			return inj, fmt.Errorf("gamepads unavailable (keyboard and mouse still work): %w", err)
		}
		inj.vigem = v
	}
	return inj, nil
}

func platformCheck(opts Options) Probe {
	var p Probe
	if err := procSendInput.Find(); err != nil {
		p.KeyboardMouse = err
	}
	if opts.Gamepads {
		v, err := newViGEmClient()
		if err != nil {
			p.Gamepads = err
		} else {
			v.close()
		}
	}
	return p
}

func (w *winInjector) GamepadConnect(idx int, name string) error {
	if !w.opts.Gamepads {
		return nil
	}
	if idx < 0 || idx >= w.opts.MaxGamepads {
		return fmt.Errorf("input: gamepad index %d out of range", idx)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.vigem == nil {
		return w.padErr
	}
	if _, ok := w.pads[idx]; ok {
		return nil
	}
	pad, err := w.vigem.addX360()
	if err != nil {
		return fmt.Errorf("input: plug in virtual controller %d: %w", idx, err)
	}
	w.pads[idx] = pad
	w.log.Info("virtual Xbox 360 controller connected", "index", idx, "client_device", name)
	return nil
}

func (w *winInjector) GamepadDisconnect(idx int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	pad, ok := w.pads[idx]
	if !ok {
		return nil
	}
	delete(w.pads, idx)
	pad.remove()
	w.log.Info("virtual controller removed", "index", idx)
	return nil
}

func (w *winInjector) GamepadState(idx int, st protocol.GamepadState) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	pad, ok := w.pads[idx]
	if !ok {
		return nil
	}
	return pad.update(toXUSB(st))
}

func (w *winInjector) Key(code string, down bool) error {
	if !w.opts.Keyboard {
		return nil
	}
	ev, ok := keyboardEvent(code, down)
	if !ok {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if down {
		w.keys[code] = true
	} else {
		delete(w.keys, code)
	}
	return sendInput(ev)
}

func (w *winInjector) MouseMove(dx, dy int32) error {
	if !w.opts.Mouse || (dx == 0 && dy == 0) {
		return nil
	}
	return sendInput(mouseEvent(mouseInput{dx: dx, dy: dy, flags: mouseeventfMove}))
}

func (w *winInjector) MouseButton(button uint8, down bool) error {
	if !w.opts.Mouse {
		return nil
	}
	ev, ok := mouseButtonEvent(button, down)
	if !ok {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if down {
		w.buttons[button] = true
	} else {
		delete(w.buttons, button)
	}
	return sendInput(ev)
}

func (w *winInjector) MouseWheel(dx, dy int32) error {
	if !w.opts.Mouse {
		return nil
	}
	var evs []rawInput
	if dy != 0 {
		evs = append(evs, mouseEvent(mouseInput{mouseData: uint32(dy * wheelDelta), flags: mouseeventfWheel}))
	}
	if dx != 0 {
		evs = append(evs, mouseEvent(mouseInput{mouseData: uint32(dx * wheelDelta), flags: mouseeventfHWheel}))
	}
	return sendInput(evs...)
}

func (w *winInjector) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var evs []rawInput
	for code := range w.keys {
		if ev, ok := keyboardEvent(code, false); ok {
			evs = append(evs, ev)
		}
	}
	for b := range w.buttons {
		if ev, ok := mouseButtonEvent(b, false); ok {
			evs = append(evs, ev)
		}
	}
	var errs []error
	if len(evs) > 0 {
		errs = append(errs, sendInput(evs...))
	}
	for idx, pad := range w.pads {
		pad.remove()
		delete(w.pads, idx)
	}
	if w.vigem != nil {
		w.vigem.close()
		w.vigem = nil
	}
	return errors.Join(errs...)
}

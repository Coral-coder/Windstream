//go:build darwin

package input

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/coral-coder/windstream/internal/macos"
	"github.com/coral-coder/windstream/internal/protocol"
)

var errNoGamepads = errors.New("input: game controllers are not supported on macOS")

func newPlatform(opts Options, log *slog.Logger) (Injector, error) {
	if !macos.AccessibilityAllowed() {
		// Ask once; events are dropped by macOS until the user allows it.
		macos.RequestAccessibility()
		log.Warn("macOS has not allowed Windstream to control the mouse and keyboard yet: " +
			"System Settings → Privacy & Security → Accessibility → turn on Windstream")
	}
	return &macInjector{opts: opts, keys: map[string]bool{}, buttons: map[uint8]bool{}}, nil
}

func platformCheck(Options) Probe {
	p := Probe{Gamepads: errNoGamepads}
	if !macos.AccessibilityAllowed() {
		p.KeyboardMouse = errors.New("allow Windstream in System Settings → Privacy & Security → Accessibility")
	}
	return p
}

type macInjector struct {
	opts Options

	mu      sync.Mutex
	closed  bool
	keys    map[string]bool // held keys
	buttons map[uint8]bool  // held mouse buttons (W3C numbering)
	flags   uint64          // held modifiers

	// Double-click detection: macOS apps read the click count from the
	// event, so it has to be supplied.
	lastBtn   uint8
	lastDown  time.Time
	lastPos   macos.Point
	clicks    int64
	desk      macos.Rect
	deskUntil time.Time
}

func (m *macInjector) GamepadConnect(int, string) error              { return errNoGamepads }
func (m *macInjector) GamepadDisconnect(int) error                   { return nil }
func (m *macInjector) GamepadState(int, protocol.GamepadState) error { return nil }

func (m *macInjector) Key(code string, down bool) error {
	if !m.opts.Keyboard {
		return nil
	}
	vk, ok := macKeyCodes[code]
	if !ok {
		return nil // a key the Mac does not have
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	_, modifier := macModifierFlags[code]
	if down {
		m.keys[code] = true
	} else {
		delete(m.keys, code)
	}
	if modifier {
		m.flags = 0
		for k := range m.keys {
			m.flags |= macModifierFlags[k]
		}
	}
	return macos.PostKey(vk, down, modifier, m.flags)
}

// desktop is the bounding box of all displays (cached briefly: the cursor
// may not leave it).
func (m *macInjector) desktop() macos.Rect {
	if time.Now().Before(m.deskUntil) {
		return m.desk
	}
	ds, err := macos.Displays()
	if err != nil || len(ds) == 0 {
		return m.desk
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, d := range ds {
		minX, minY = math.Min(minX, d.Bounds.Origin.X), math.Min(minY, d.Bounds.Origin.Y)
		maxX, maxY = math.Max(maxX, d.Bounds.Origin.X+d.Bounds.W), math.Max(maxY, d.Bounds.Origin.Y+d.Bounds.H)
	}
	m.desk = macos.Rect{Origin: macos.Point{X: minX, Y: minY}, W: maxX - minX, H: maxY - minY}
	m.deskUntil = time.Now().Add(2 * time.Second)
	return m.desk
}

// moveTo moves the cursor to p (clamped to the screens), as a drag while a
// button is held.
func (m *macInjector) moveTo(p, from macos.Point) error {
	d := m.desktop()
	if d.W > 0 && d.H > 0 {
		p.X = math.Max(d.Origin.X, math.Min(p.X, d.Origin.X+d.W-1))
		p.Y = math.Max(d.Origin.Y, math.Min(p.Y, d.Origin.Y+d.H-1))
	}
	typ, btn := uint32(macos.EventMouseMoved), uint32(0)
	switch {
	case m.buttons[0]:
		typ = macos.EventLeftDragged
	case m.buttons[2]:
		typ, btn = macos.EventRightDragged, 1
	case len(m.buttons) > 0:
		typ, btn = macos.EventOtherDragged, 2
	}
	return macos.PostMouse(typ, p, btn, m.flags, map[uint32]int64{
		macos.FieldDeltaX: int64(math.Round(p.X - from.X)),
		macos.FieldDeltaY: int64(math.Round(p.Y - from.Y)),
	})
}

func (m *macInjector) MouseMove(dx, dy int32) error {
	if !m.opts.Mouse || (dx == 0 && dy == 0) {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	cur, err := macos.CursorLocation()
	if err != nil {
		return err
	}
	return m.moveTo(macos.Point{X: cur.X + float64(dx), Y: cur.Y + float64(dy)}, cur)
}

func (m *macInjector) MouseMoveAbs(x, y int32) error {
	if !m.opts.Mouse {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	d := m.desktop()
	if d.W <= 0 || d.H <= 0 {
		return errors.New("no display")
	}
	cur, _ := macos.CursorLocation()
	return m.moveTo(macos.Point{
		X: d.Origin.X + float64(x)*(d.W-1)/65535,
		Y: d.Origin.Y + float64(y)*(d.H-1)/65535,
	}, cur)
}

func (m *macInjector) MouseButton(button uint8, down bool) error {
	if !m.opts.Mouse {
		return nil
	}
	btn, ok := macButtons[button]
	if !ok {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	return m.button(button, btn, down)
}

func (m *macInjector) button(button uint8, btn uint32, down bool) error {
	pos, err := macos.CursorLocation()
	if err != nil {
		return err
	}
	if down {
		near := math.Abs(pos.X-m.lastPos.X) < 5 && math.Abs(pos.Y-m.lastPos.Y) < 5
		if button == m.lastBtn && near && time.Since(m.lastDown) < 500*time.Millisecond {
			m.clicks++
		} else {
			m.clicks = 1
		}
		m.lastBtn, m.lastDown, m.lastPos = button, time.Now(), pos
		m.buttons[button] = true
	} else {
		delete(m.buttons, button)
	}
	var typ uint32
	switch {
	case btn == 0 && down:
		typ = macos.EventLeftDown
	case btn == 0:
		typ = macos.EventLeftUp
	case btn == 1 && down:
		typ = macos.EventRightDown
	case btn == 1:
		typ = macos.EventRightUp
	case down:
		typ = macos.EventOtherDown
	default:
		typ = macos.EventOtherUp
	}
	return macos.PostMouse(typ, pos, btn, m.flags, map[uint32]int64{
		macos.FieldClickState:   m.clicks,
		macos.FieldButtonNumber: int64(btn),
	})
}

func (m *macInjector) MouseWheel(dx, dy int32) error {
	if !m.opts.Mouse || (dx == 0 && dy == 0) {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	return macos.PostScroll(dy, -dx, m.flags)
}

// Close releases everything this client still holds.
func (m *macInjector) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	var errs []error
	for b := range m.buttons {
		if btn, ok := macButtons[b]; ok {
			errs = append(errs, m.button(b, btn, false))
		}
	}
	for code := range m.keys {
		delete(m.keys, code)
		_, modifier := macModifierFlags[code]
		if modifier {
			m.flags &^= macModifierFlags[code]
		}
		if vk, ok := macKeyCodes[code]; ok {
			errs = append(errs, macos.PostKey(vk, false, modifier, m.flags))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("releasing input: %w", err)
	}
	return nil
}

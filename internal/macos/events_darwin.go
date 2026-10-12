//go:build darwin

package macos

import (
	"errors"
	"sync"
)

// CGEvent types and fields used to synthesise input.
const (
	EventLeftDown     = 1
	EventLeftUp       = 2
	EventRightDown    = 3
	EventRightUp      = 4
	EventMouseMoved   = 5
	EventLeftDragged  = 6
	EventRightDragged = 7
	EventFlagsChanged = 12
	EventOtherDown    = 25
	EventOtherUp      = 26
	EventOtherDragged = 27

	FieldClickState   = 1
	FieldButtonNumber = 3
	FieldDeltaX       = 4
	FieldDeltaY       = 5

	FlagCapsLock = 0x00010000
	FlagShift    = 0x00020000
	FlagControl  = 0x00040000
	FlagOption   = 0x00080000
	FlagCommand  = 0x00100000

	hidSystemState = 1 // kCGEventSourceStateHIDSystemState
	hidEventTap    = 0 // kCGHIDEventTap
	scrollLine     = 1 // kCGScrollEventUnitLine
)

var (
	srcOnce sync.Once
	src     uintptr
)

func source() uintptr {
	srcOnce.Do(func() {
		if load() == nil {
			src = cgEventSourceCreate(hidSystemState)
		}
	})
	return src
}

// ErrNoEvents means the event APIs could not be loaded.
var ErrNoEvents = errors.New("macOS input APIs unavailable")

// CursorLocation returns the mouse position in global points.
func CursorLocation() (Point, error) {
	if err := load(); err != nil {
		return Point{}, err
	}
	ev := cgEventCreate(0)
	if ev == 0 {
		return Point{}, ErrNoEvents
	}
	defer cfRelease(ev)
	return cgEventGetLocation(ev), nil
}

// PostMouse posts a mouse event of type typ at pos.
func PostMouse(typ uint32, pos Point, button uint32, flags uint64, fields map[uint32]int64) error {
	if err := load(); err != nil {
		return err
	}
	ev := cgEventCreateMouseEvent(source(), typ, pos, button)
	if ev == 0 {
		return ErrNoEvents
	}
	defer cfRelease(ev)
	for f, v := range fields {
		cgEventSetIntegerValueField(ev, f, v)
	}
	cgEventSetFlags(ev, flags)
	cgEventPost(hidEventTap, ev)
	return nil
}

// PostKey posts a key press or release. Modifier keys are posted as
// flags-changed events, the way a real keyboard reports them.
func PostKey(code uint16, down, modifier bool, flags uint64) error {
	if err := load(); err != nil {
		return err
	}
	ev := cgEventCreateKeyboardEvent(source(), code, down)
	if ev == 0 {
		return ErrNoEvents
	}
	defer cfRelease(ev)
	if modifier {
		cgEventSetType(ev, EventFlagsChanged)
	}
	cgEventSetFlags(ev, flags)
	cgEventPost(hidEventTap, ev)
	return nil
}

// PostScroll scrolls by whole lines (positive dy = up, positive dx = left).
func PostScroll(dy, dx int32, flags uint64) error {
	if err := load(); err != nil {
		return err
	}
	ev := cgEventCreateScrollWheelEvt2(source(), scrollLine, 2, dy, dx, 0)
	if ev == 0 {
		return ErrNoEvents
	}
	defer cfRelease(ev)
	cgEventSetFlags(ev, flags)
	cgEventPost(hidEventTap, ev)
	return nil
}

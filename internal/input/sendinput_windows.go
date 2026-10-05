//go:build windows

package input

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32        = windows.NewLazySystemDLL("user32.dll")
	procSendInput = user32.NewProc("SendInput")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	keyeventfExtendedKey = 0x0001
	keyeventfKeyUp       = 0x0002
	keyeventfScanCode    = 0x0008

	mouseeventfMove       = 0x0001
	mouseeventfLeftDown   = 0x0002
	mouseeventfLeftUp     = 0x0004
	mouseeventfRightDown  = 0x0008
	mouseeventfRightUp    = 0x0010
	mouseeventfMiddleDown = 0x0020
	mouseeventfMiddleUp   = 0x0040
	mouseeventfXDown      = 0x0080
	mouseeventfXUp        = 0x0100
	mouseeventfWheel      = 0x0800
	mouseeventfHWheel     = 0x1000

	wheelDelta = 120
)

// mouseInput mirrors MOUSEINPUT (32 bytes on 64-bit).
type mouseInput struct {
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	extraInfo uintptr
}

// keybdInput mirrors KEYBDINPUT (24 bytes on 64-bit).
type keybdInput struct {
	vk, scan  uint16
	flags     uint32
	time      uint32
	extraInfo uintptr
}

// rawInput mirrors INPUT: a DWORD type followed by the union, whose largest
// member is MOUSEINPUT.
type rawInput struct {
	typ   uint32
	_     uint32
	union [unsafe.Sizeof(mouseInput{})]byte
}

func mouseEvent(m mouseInput) rawInput {
	in := rawInput{typ: inputMouse}
	*(*mouseInput)(unsafe.Pointer(&in.union[0])) = m
	return in
}

func keyEvent(k keybdInput) rawInput {
	in := rawInput{typ: inputKeyboard}
	*(*keybdInput)(unsafe.Pointer(&in.union[0])) = k
	return in
}

func sendInput(events ...rawInput) error {
	if len(events) == 0 {
		return nil
	}
	n, _, err := procSendInput.Call(uintptr(len(events)), uintptr(unsafe.Pointer(&events[0])), unsafe.Sizeof(events[0]))
	if int(n) != len(events) {
		// Blocked by UIPI (target window is elevated and we are not) or the
		// secure desktop (UAC prompt, lock screen) is active.
		return fmt.Errorf("SendInput injected %d/%d events: %v", n, len(events), err)
	}
	return nil
}

func keyboardEvent(code string, down bool) (rawInput, bool) {
	var flags uint32
	if !down {
		flags |= keyeventfKeyUp
	}
	if sc, ext, ok := ScanCode(code); ok {
		flags |= keyeventfScanCode
		if ext {
			flags |= keyeventfExtendedKey
		}
		return keyEvent(keybdInput{scan: sc, flags: flags}), true
	}
	if vk, ok := virtualKeys[code]; ok {
		return keyEvent(keybdInput{vk: vk, flags: flags}), true
	}
	return rawInput{}, false
}

var mouseButtonFlags = map[uint8][2]uint32{ // W3C MouseEvent.button -> {down, up}
	0: {mouseeventfLeftDown, mouseeventfLeftUp},
	1: {mouseeventfMiddleDown, mouseeventfMiddleUp},
	2: {mouseeventfRightDown, mouseeventfRightUp},
	3: {mouseeventfXDown, mouseeventfXUp},
	4: {mouseeventfXDown, mouseeventfXUp},
}

func mouseButtonEvent(button uint8, down bool) (rawInput, bool) {
	f, ok := mouseButtonFlags[button]
	if !ok {
		return rawInput{}, false
	}
	m := mouseInput{flags: f[1]}
	if down {
		m.flags = f[0]
	}
	switch button {
	case 3:
		m.mouseData = 1 // XBUTTON1 (back)
	case 4:
		m.mouseData = 2 // XBUTTON2 (forward)
	}
	return mouseEvent(m), true
}

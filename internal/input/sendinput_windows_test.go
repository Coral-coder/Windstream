//go:build windows

package input

import (
	"testing"
	"unsafe"
)

func TestInputLayout(t *testing.T) {
	// INPUT is 40 bytes on 64-bit Windows; SendInput rejects any other cbSize.
	if unsafe.Sizeof(uintptr(0)) == 8 && unsafe.Sizeof(rawInput{}) != 40 {
		t.Fatalf("sizeof(INPUT) = %d, want 40", unsafe.Sizeof(rawInput{}))
	}
	ev, ok := keyboardEvent("ArrowUp", false)
	k := (*keybdInput)(unsafe.Pointer(&ev.union[0]))
	if !ok || ev.typ != inputKeyboard || k.scan != 0x48 || k.flags != keyeventfScanCode|keyeventfExtendedKey|keyeventfKeyUp {
		t.Fatalf("ArrowUp up = %+v / %+v", ev, *k)
	}
}

package input

import "testing"

func TestMacKeyMap(t *testing.T) {
	// Spot checks against Carbon's kVK_* constants.
	for code, want := range map[string]uint16{
		"KeyA": 0x00, "KeyZ": 0x06, "KeyC": 0x08, "KeyV": 0x09, "Digit1": 0x12, "Digit0": 0x1D,
		"Enter": 0x24, "Space": 0x31, "Escape": 0x35, "MetaLeft": 0x37, "ShiftLeft": 0x38,
		"Backspace": 0x33, "Delete": 0x75, "ArrowUp": 0x7E, "F1": 0x7A, "F12": 0x6F,
	} {
		if got, ok := macKeyCodes[code]; !ok || got != want {
			t.Errorf("%s = %#x, want %#x", code, got, want)
		}
	}
	// Every code a browser sends for a normal key must be known, and no two
	// keys may land on the same Mac key (Insert and Help are the same key).
	seen := map[uint16]string{}
	for code, vk := range macKeyCodes {
		if prev, dup := seen[vk]; dup && !(vk == 0x72) {
			t.Errorf("%s and %s both map to %#x", prev, code, vk)
		}
		seen[vk] = code
	}
	for _, c := range []string{"KeyQ", "Tab", "CapsLock", "AltLeft", "ControlRight", "ArrowLeft", "PageDown", "Home", "End", "Backquote", "Slash"} {
		if _, ok := macKeyCodes[c]; !ok {
			t.Errorf("%s missing", c)
		}
	}
	for code := range macModifierFlags {
		if _, ok := macKeyCodes[code]; !ok {
			t.Errorf("modifier %s has no key code", code)
		}
	}
}

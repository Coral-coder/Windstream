package input

import (
	"testing"
	"unsafe"

	"github.com/coral-coder/windstream/internal/protocol"
)

func TestScanCodes(t *testing.T) {
	cases := map[string]scanCode{
		"KeyA": {0x1E, false}, "Space": {0x39, false}, "Enter": {0x1C, false},
		"NumpadEnter": {0x1C, true}, "ArrowUp": {0x48, true}, "Numpad8": {0x48, false},
		"ControlRight": {0x1D, true}, "MetaLeft": {0x5B, true}, "F12": {0x58, false},
	}
	for code, want := range cases {
		sc, ext, ok := ScanCode(code)
		if !ok || sc != want.code || ext != want.extended {
			t.Errorf("ScanCode(%s) = %#x,%v,%v want %#x,%v", code, sc, ext, ok, want.code, want.extended)
		}
	}
	if _, _, ok := ScanCode("Bogus"); ok {
		t.Error("unknown code resolved")
	}
	seen := map[scanCode]string{}
	for code, sc := range scanCodes {
		if prev, dup := seen[sc]; dup {
			t.Errorf("duplicate scan code %#x/%v for %s and %s", sc.code, sc.extended, code, prev)
		}
		seen[sc] = code
	}
}

func TestToXUSB(t *testing.T) {
	if unsafe.Sizeof(xusbReport{}) != 12 {
		t.Fatalf("XUSB_REPORT must be 12 bytes, got %d", unsafe.Sizeof(xusbReport{}))
	}
	st := protocol.GamepadState{
		Buttons:  1<<protocol.BtnA | 1<<protocol.BtnDUp | 1<<protocol.BtnGuide | 1<<protocol.BtnLT,
		Axes:     [4]int16{100, -32768, -5, 32767},
		Triggers: [2]uint8{200, 0},
	}
	r := toXUSB(st)
	if r.Buttons != xusbA|xusbDPadUp|xusbGuide {
		t.Errorf("buttons = %#x (analog LT must not map to a button)", r.Buttons)
	}
	if r.ThumbLX != 100 || r.ThumbLY != 32767 || r.ThumbRX != -5 || r.ThumbRY != -32767 {
		t.Errorf("axes = %d %d %d %d (Y must be inverted)", r.ThumbLX, r.ThumbLY, r.ThumbRX, r.ThumbRY)
	}
	if r.LeftTrigger != 200 || r.RightTrigger != 0 {
		t.Errorf("triggers = %d %d", r.LeftTrigger, r.RightTrigger)
	}
}

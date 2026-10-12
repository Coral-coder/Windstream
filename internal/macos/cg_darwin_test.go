//go:build darwin

package macos

import "testing"

// These run on the CI's Mac: they prove the framework bindings (struct
// returns included) work on real macOS, without needing any permission.
func TestDisplaysAndCursor(t *testing.T) {
	ds, err := Displays()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d display(s)", len(ds))
	for _, d := range ds {
		t.Logf("display %d: %+v px=%dx%d main=%v builtin=%v", d.ID, d.Bounds, d.PixelsWide, d.PixelsHigh, d.Main, d.Builtin)
		if d.Bounds.W <= 0 || d.Bounds.H <= 0 || d.PixelsWide <= 0 {
			t.Errorf("implausible display %+v", d)
		}
	}
	p, err := CursorLocation()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cursor at %+v", p)
	t.Logf("screen recording allowed: %v, accessibility allowed: %v", ScreenRecordingAllowed(), AccessibilityAllowed())
}

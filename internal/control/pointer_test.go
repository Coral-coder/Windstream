package control

import (
	"image"
	"testing"
)

func TestMapPointer(t *testing.T) {
	// Streamed 1920x1080 monitor on the right of a 2560x1440 primary,
	// offset down by 200: the desktop is 4480 wide, 1440 high.
	primary := image.Rect(0, 0, 2560, 1440)
	streamed := image.Rect(2560, 200, 4480, 1280)
	desk := primary.Union(streamed)
	toPixels := func(nx, ny int32) (int, int) {
		return desk.Min.X + int(int64(nx)*int64(desk.Dx()-1)/65535+0),
			desk.Min.Y + int(int64(ny)*int64(desk.Dy()-1)/65535)
	}
	for _, tc := range []struct {
		x, y   uint16
		px, py int
	}{
		{0, 0, 2560, 200},          // top-left of the streamed screen
		{65535, 65535, 4479, 1279}, // bottom-right
		{32768, 32768, 3519, 739},  // middle
	} {
		px, py := toPixels(mapPointer(tc.x, tc.y, streamed, desk))
		if abs(px-tc.px) > 1 || abs(py-tc.py) > 1 {
			t.Errorf("(%d,%d) -> pixel (%d,%d), want (%d,%d)", tc.x, tc.y, px, py, tc.px, tc.py)
		}
	}
	// A monitor left of the primary gives negative desktop coordinates.
	left := image.Rect(-1920, 0, 0, 1080)
	desk = primary.Union(left)
	nx, ny := mapPointer(0, 0, left, desk)
	if nx != 0 || ny != 0 {
		t.Errorf("left monitor top-left -> %d,%d", nx, ny)
	}
	nx, _ = mapPointer(0, 0, primary, desk)
	if want := int32(1920 * 65535 / (desk.Dx() - 1)); nx != want {
		t.Errorf("primary top-left x = %d, want %d", nx, want)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

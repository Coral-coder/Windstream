package display

import "testing"

func TestFitMode(t *testing.T) {
	monitor := []Mode{
		{3840, 2160, 60}, {3840, 2160, 30}, {2560, 1440, 60}, {2560, 1440, 120}, {1920, 1080, 60},
		{1920, 1080, 144}, {1920, 1200, 60}, {1600, 900, 60}, {1280, 1024, 60}, {1280, 720, 60}, {800, 600, 60},
	}
	cur := Mode{3840, 2160, 60}
	for _, c := range []struct {
		name       string
		maxW, maxH int
		want       Mode
		ok         bool
	}{
		{"no limit keeps 4K", 0, 0, cur, true},
		{"limit above the current mode keeps it", 7680, 4320, cur, true},
		{"1440p viewer", 2560, 1440, Mode{2560, 1440, 60}, true},
		{"1080p viewer: same shape, same refresh", 1920, 1080, Mode{1920, 1080, 60}, true},
		{"a 1920x1200 laptop still gets 16:9", 1920, 1200, Mode{1920, 1080, 60}, true},
		{"phone in landscape (2340x1080)", 2340, 1080, Mode{1920, 1080, 60}, true},
		{"width only", 1700, 0, Mode{1600, 900, 60}, true},
		{"nothing fits", 640, 480, Mode{}, false},
	} {
		got, ok := FitMode(monitor, cur, c.maxW, c.maxH)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
	// Another shape only when no mode of the current shape fits.
	if got, _ := FitMode([]Mode{{1280, 1024, 60}, {1920, 1080, 60}}, cur, 1300, 1100); got != (Mode{1280, 1024, 60}) {
		t.Errorf("fallback shape: %v", got)
	}
}

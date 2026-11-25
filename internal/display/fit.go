package display

// Mode is a display resolution and refresh rate.
type Mode struct {
	Width, Height, Refresh int
}

// FitMode picks the mode a display should switch to so the picture is no
// bigger than maxW x maxH (0 = no limit on that side): cur itself when it
// already fits, otherwise the largest of modes that fits, preferring cur's
// shape (aspect ratio) and the refresh rate closest to cur's (not above).
// ok is false when no mode fits; then the display is left as it is.
func FitMode(modes []Mode, cur Mode, maxW, maxH int) (Mode, bool) {
	fits := func(m Mode) bool {
		return m.Width > 0 && m.Height > 0 && (maxW <= 0 || m.Width <= maxW) && (maxH <= 0 || m.Height <= maxH)
	}
	if fits(cur) {
		return cur, true
	}
	sameShape := func(m Mode) bool {
		// Within 2%: 1366x768 counts as 16:9.
		a, b := m.Width*cur.Height, cur.Width*m.Height
		d := a - b
		if d < 0 {
			d = -d
		}
		return d*50 <= b
	}
	better := func(m, best Mode) bool {
		if s, bs := sameShape(m), sameShape(best); s != bs {
			return s
		}
		if a, ba := m.Width*m.Height, best.Width*best.Height; a != ba {
			return a > ba
		}
		// Same size: the refresh closest to the current one, not above it.
		mr, br := refreshScore(m.Refresh, cur.Refresh), refreshScore(best.Refresh, cur.Refresh)
		return mr < br
	}
	var best Mode
	found := false
	for _, m := range modes {
		if fits(m) && (!found || better(m, best)) {
			best, found = m, true
		}
	}
	return best, found
}

// refreshScore orders refresh rates: equal to want is best, then lower
// ones by closeness, then higher ones.
func refreshScore(r, want int) int {
	if r <= want {
		return want - r
	}
	return 1000 + r - want
}

package winapp

import (
	"strconv"
	"strings"
)

// newerVersion reports whether release a is later than release b, comparing
// dotted numbers ("1.0.11" > "1.0.9"). A version that is not purely numeric
// (e.g. "dev") is never considered newer or older than anything.
func newerVersion(a, b string) bool {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func parseVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 { // "dev", a commit hash, ... are not releases
		return nil, false
	}
	var out []int
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

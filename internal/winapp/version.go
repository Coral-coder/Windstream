package winapp

import "github.com/coral-coder/windstream/internal/appver"

// newerVersion reports whether release a is later than release b.
func newerVersion(a, b string) bool { return appver.Newer(a, b) }

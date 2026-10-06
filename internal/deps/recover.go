package deps

import (
	"fmt"
	"runtime/debug"
)

// guard converts a panic in an install step into an error, so one broken
// dependency (e.g. a driver install that hits an unexpected OS condition)
// never crashes the whole app.
func guard(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v\n%s", r, debug.Stack())
		}
	}()
	return fn()
}

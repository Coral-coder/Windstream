// Package safe keeps one failing goroutine from taking down the server: a Go
// panic in a goroutine that does not recover it terminates the whole
// process, so every goroutine and library callback Windstream starts recovers
// and logs instead.
package safe

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// Recover logs a panic instead of letting it crash the process. It must be
// deferred directly: defer safe.Recover(log, "what").
func Recover(log *slog.Logger, what string) {
	if r := recover(); r != nil {
		logPanic(log, what, r)
	}
}

// Go runs fn in a goroutine that recovers from panics.
func Go(log *slog.Logger, what string, fn func()) {
	go func() {
		defer Recover(log, what)
		fn()
	}()
}

// Call runs fn and reports whether it panicked (the panic is logged).
func Call(log *slog.Logger, what string, fn func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			logPanic(log, what, r)
			panicked = true
		}
	}()
	fn()
	return false
}

func logPanic(log *slog.Logger, what string, r any) {
	if log == nil {
		log = slog.Default()
	}
	log.Error("internal error recovered; continuing", "where", what, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
}

package control

import (
	"log/slog"
	"runtime/debug"
)

// safego runs fn in a goroutine that logs and swallows a panic instead of
// crashing the whole process. Used for every background worker (dependency
// setup, network, display) so one failure never takes down the dashboard.
func safego(log *slog.Logger, name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("background task crashed (recovered)", "task", name, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}

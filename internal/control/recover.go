package control

import (
	"log/slog"

	"github.com/coral-coder/windstream/internal/safe"
)

// safego runs fn in a goroutine that logs and swallows a panic instead of
// crashing the whole process. Used for every background worker (dependency
// setup, network, display) so one failure never takes down the dashboard.
func safego(log *slog.Logger, name string, fn func()) { safe.Go(log, name, fn) }

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/coral-coder/windstream/internal/control"
)

type appOptions struct {
	DataDir   string
	PanelAddr string
	Dev       bool
	Stderr    bool // also log to stderr
	Platform  control.Platform
	OnReady   func(c *control.Controller)
	OnCrash   func(msg, logPath string) // shown to the user on a fatal crash
}

// runApp starts the controller with logging to <data>/logs/windstream.log
// and the dashboard's in-memory log view.
func runApp(ctx context.Context, o appOptions) error {
	if err := os.MkdirAll(filepath.Join(o.DataDir, "logs"), 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(o.DataDir, "logs", "windstream.log")
	if st, err := os.Stat(logPath); err == nil && st.Size() > 20<<20 {
		_ = os.Rename(logPath, logPath+".1")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := control.NewLogBuffer(400)
	writers := []io.Writer{f, buf}
	if o.Stderr {
		writers = append(writers, os.Stderr)
	}
	log := slog.New(slog.NewTextHandler(io.MultiWriter(writers...), &slog.HandlerOptions{Level: slog.LevelInfo}))

	c, err := control.New(control.Options{
		DataDir: o.DataDir, PanelAddr: o.PanelAddr, Version: version,
		Log: log, Logs: buf, Platform: o.Platform, Dev: o.Dev,
	})
	if err != nil {
		log.Error("startup failed", "error", err)
		return err
	}
	if o.OnReady != nil {
		o.OnReady(c)
	}
	// A panic escaping Run is written to the log (and, on Windows, shown)
	// before the process exits, so a crash is never silent.
	defer func() {
		if r := recover(); r != nil {
			log.Error("windstream crashed", "panic", r, "stack", string(debug.Stack()))
			_ = f.Sync()
			if o.OnCrash != nil {
				o.OnCrash(fmt.Sprintf("%v", r), logPath)
			}
			os.Exit(1)
		}
	}()
	return c.Run(ctx)
}

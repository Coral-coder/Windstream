package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/coral-coder/windstream/internal/control"
)

const defaultPanelAddr = "127.0.0.1:47333"

type appOptions struct {
	DataDir   string
	PanelAddr string
	Dev       bool
	Stderr    bool // also log to stderr
	Platform  control.Platform
	OnReady   func(c *control.Controller)
}

// runApp starts the controller with logging to <data>/logs/windstream.log
// and the dashboard's in-memory log view.
func runApp(ctx context.Context, o appOptions) error {
	if o.PanelAddr == "" {
		o.PanelAddr = defaultPanelAddr
	}
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
	return c.Run(ctx)
}

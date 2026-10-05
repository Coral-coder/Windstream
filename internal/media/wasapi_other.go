//go:build !windows

package media

import (
	"context"
	"io"
	"log/slog"
)

func runLoopback(context.Context, string, *slog.Logger, func(PCMFormat, io.Reader)) error {
	return ErrNoWASAPI
}

// ListPlaybackDevices lists WASAPI render endpoints (Windows only).
func ListPlaybackDevices() ([]string, error) { return nil, ErrNoWASAPI }

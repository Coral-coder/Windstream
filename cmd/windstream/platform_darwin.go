//go:build darwin

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/coral-coder/windstream/internal/control"
	"github.com/coral-coder/windstream/internal/macapp"
)

// platformMain handles how the Mac app is started: opened from Finder (no
// arguments), as the login item (--agent), or by the agent to check
// permissions (--tcc-check). Anything else is the command line.
func platformMain(args []string) bool {
	macapp.Version = version
	// Older macOS passes a process serial number when opened from Finder.
	clean := args[:0:0]
	for _, a := range args {
		if !strings.HasPrefix(a, "-psn_") {
			clean = append(clean, a)
		}
	}
	run := func(ctx context.Context, p control.Platform, onReady func(*control.Controller)) error {
		return runApp(ctx, appOptions{DataDir: macapp.DataDir(), Platform: p, OnReady: onReady})
	}
	var err error
	switch {
	case len(clean) == 0:
		err = macapp.Launch(run)
	case clean[0] == "--agent":
		err = macapp.Agent(run)
	case clean[0] == "--tcc-check":
		macapp.CheckPermissions()
	default:
		return false
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "windstream:", err)
		os.Exit(1)
	}
	return true
}

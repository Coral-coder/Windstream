//go:build windows

package main

import (
	"context"
	"os"

	"github.com/coral-coder/windstream/internal/control"
	"github.com/coral-coder/windstream/internal/winapp"
)

// platformMain handles the desktop-app entry points. The exe is a GUI
// program: double-click (no arguments) installs or opens Windstream.
func platformMain(args []string) bool {
	winapp.Version = version
	panelURL := "http://" + defaultPanelAddr + "/"
	var err error
	switch {
	case len(args) == 0:
		err = winapp.Launch(panelURL)
	case args[0] == "--install":
		err = winapp.InstallFromArgs(args[1:])
	case args[0] == "--run":
		err = winapp.Resident(panelURL, func(ctx context.Context, p control.Platform, onReady func(*control.Controller)) error {
			return runApp(ctx, appOptions{DataDir: winapp.DataDir, PanelAddr: defaultPanelAddr, Platform: p, OnReady: onReady})
		})
	case args[0] == "--uninstall":
		err = winapp.Uninstall(args[1:])
	default:
		winapp.AttachParentConsole() // CLI subcommands print to the calling terminal
		return false
	}
	if err != nil {
		winapp.ShowError(err)
		os.Exit(1)
	}
	return true
}

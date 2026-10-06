//go:build windows

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/coral-coder/windstream/internal/control"
	"github.com/coral-coder/windstream/internal/deps"
	"github.com/coral-coder/windstream/internal/winapp"
)

// platformMain handles the desktop-app entry points. The exe is a GUI
// program: double-click (no arguments) installs or opens Windstream.
func platformMain(args []string) bool {
	winapp.Version = version
	var err error
	switch {
	case len(args) == 0:
		err = winapp.Launch()
	case args[0] == "--install":
		err = winapp.InstallFromArgs(args[1:])
	case args[0] == "--run":
		err = winapp.Resident(func(ctx context.Context, p control.Platform, onReady func(*control.Controller)) error {
			// Empty PanelAddr: the controller uses the saved dashboard port,
			// moving to a free one if another program holds it.
			return runApp(ctx, appOptions{DataDir: winapp.DataDir, Platform: p, OnReady: onReady, OnCrash: winapp.ShowCrash})
		})
	case args[0] == "--vdd-install":
		// Internal: install the virtual display device in a child process so
		// a native driver-subsystem crash cannot take down the main app. Its
		// stdout/stderr are piped back to the parent, so no console is attached.
		if len(args) < 2 {
			os.Exit(2)
		}
		reboot, verr := deps.InstallVDDDevice(args[1])
		if verr != nil {
			fmt.Fprintln(os.Stderr, verr.Error())
			os.Exit(1)
		}
		if reboot {
			fmt.Println("installed (restart recommended)")
		} else {
			fmt.Println("installed")
		}
		os.Exit(0)
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

//go:build windows

package winapp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ElevateSelf restarts this program through the UAC prompt when it is not
// already running as administrator, waits for that copy, and reports true.
// The caller should then exit.
func ElevateSelf() (bool, error) {
	if isElevated() {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	fmt.Println("Asking for administrator permission (Windstream's settings are protected).")
	fmt.Println("The reset continues in a new window.")
	args := make([]string, 0, len(os.Args)-1)
	for _, a := range os.Args[1:] {
		args = append(args, syscall.EscapeArg(a))
	}
	_, err = runElevated(exe, strings.Join(args, " "))
	return true, err
}

// OpenInBrowser opens a local file or URL in the user's (non-elevated)
// default browser.
func OpenInBrowser(path string) {
	if abs, err := filepath.Abs(path); err == nil && !strings.Contains(path, "://") {
		path = abs
	}
	openURL(path)
}

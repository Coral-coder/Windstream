//go:build windows

package winapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// runningVersion returns the version of the Windstream answering on the
// dashboard port, or "" if none is running.
func runningVersion(panelURL string) string {
	c := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Get(panelURL + "api/state")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var st struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&st) != nil || st.Version == "" {
		return "?"
	}
	return st.Version
}

func panelUp(panelURL string) bool { return runningVersion(panelURL) != "" }

// Launch is what happens on double-click: open the dashboard if Windstream
// is running; otherwise install/update (one UAC prompt), start it, and open
// the dashboard once it is up.
func Launch(panelURL string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// Already running: just open it, unless this exe is a different version
	// (then fall through and upgrade in place).
	if v := runningVersion(panelURL); v != "" && (v == Version || samePath(self, InstalledExe)) {
		openURL(panelURL)
		return nil
	}
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	if isElevated() {
		if err := Install(Version, sid); err != nil {
			return fmt.Errorf("Windstream could not be installed:\n\n%w", err)
		}
	} else {
		code, err := runElevated(self, "--install --user "+sid)
		if err == ErrCancelled {
			messageBox("Windstream needs administrator permission once to install its virtual screen and controller drivers and to start automatically.", mbOK|mbIconWarning)
			return nil
		}
		if err != nil {
			return err
		}
		if code != 0 {
			return nil // the elevated installer already showed its error
		}
	}
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if v := runningVersion(panelURL); v == Version || v == "?" {
			openURL(panelURL)
			return nil
		}
	}
	return fmt.Errorf("Windstream was installed but did not start. The log is in %s\\logs", DataDir)
}

// Version is set by the main package.
var Version = "dev"

// InstallFromArgs implements `--install --user SID` (run elevated by Launch).
func InstallFromArgs(args []string) error {
	sid := ""
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--user" {
			sid = args[i+1]
		}
	}
	if err := Install(Version, sid); err != nil {
		return fmt.Errorf("Windstream could not be installed:\n\n%w", err)
	}
	return nil
}

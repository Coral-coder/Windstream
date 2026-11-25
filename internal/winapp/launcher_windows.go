//go:build windows

package winapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
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
		Version     *string `json:"version"`
		SetupNeeded *bool   `json:"setup_needed"`
	}
	// Anything else answering on this port is not Windstream.
	if json.NewDecoder(resp.Body).Decode(&st) != nil || st.Version == nil || st.SetupNeeded == nil {
		return ""
	}
	return *st.Version
}

// PanelURL is where the running Windstream's dashboard listens: the address
// it last published in the registry, or the default.
func PanelURL() string {
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, appRegKey, registry.QUERY_VALUE); err == nil {
		defer k.Close()
		if v, _, err := k.GetStringValue("PanelURL"); err == nil && strings.HasPrefix(v, "http://127.0.0.1:") {
			return v
		}
	}
	return DefaultPanelURL
}

// DefaultPanelURL is used before Windstream has ever started.
const DefaultPanelURL = "http://127.0.0.1:47333/"

// runningApp returns the version of the Windstream running on this PC, or
// "". Versions with the Windstream window record it in the registry; older
// ones answer on their browser dashboard.
func runningApp() string {
	if !residentRunning() {
		return ""
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, appRegKey, registry.QUERY_VALUE); err == nil {
		v, _, verr := k.GetStringValue("RunningVersion")
		k.Close()
		if verr == nil && v != "" {
			return v
		}
	}
	return runningVersion(PanelURL())
}

// residentRunning reports whether the tray app (and with it the server)
// is running in this session. Its mutex belongs to an elevated process, so
// an unelevated launcher is refused access to it, which says just as well
// that it exists.
func residentRunning() bool {
	name, _ := windows.UTF16PtrFromString(residentMutex)
	m, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err == nil {
		windows.CloseHandle(m)
		return true
	}
	return errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

// openRunning shows the running Windstream: its window, or for an older
// version still running, its browser dashboard.
func openRunning() {
	for i := 0; i < 40; i++ {
		if ShowPanel() == nil {
			return
		}
		if url := PanelURL(); url != "" && runningVersion(url) != "" {
			openURL(url)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	messageBox("Windstream is still starting. Try again in a few seconds.", mbOK|mbIconInfo)
}

// Launch is what happens on double-click: show Windstream if it is
// running; otherwise install/update (one UAC prompt), start it, and show
// its window once it is up.
func Launch() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	running := runningApp()
	// Never downgrade: an old copy of Windstream.exe (e.g. an earlier
	// download) opens the newer installed version instead of replacing it.
	// An older version cannot read settings written by a newer one.
	newest := running
	if iv := installedVersion(); newest == "" || newerVersion(iv, newest) {
		newest = iv
	}
	if newerVersion(newest, Version) && !samePath(self, InstalledExe) {
		if _, err := os.Stat(InstalledExe); err == nil || running != "" {
			messageBox(fmt.Sprintf("This is an older copy of Windstream (%s). Windstream %s is already installed, so that is being opened instead.\n\nYou can delete this older file.", Version, newest), mbOK|mbIconInfo)
			if running != "" {
				openRunning()
				return nil
			}
			return exec.Command(InstalledExe).Start()
		}
		// The installed exe is gone: reinstall from this copy.
	}
	// Already running: just open it, unless this exe is a different version
	// (then fall through and upgrade in place).
	if running != "" && (running == Version || samePath(self, InstalledExe)) {
		openRunning()
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
		if runningApp() == Version && ShowPanel() == nil {
			return nil
		}
	}
	return fmt.Errorf("Windstream was installed but did not start. The log is in %s\\logs", DataDir)
}

// installedVersion is the version recorded when Windstream was installed,
// or "" if it is not installed.
func installedVersion() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue("DisplayVersion")
	return v
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

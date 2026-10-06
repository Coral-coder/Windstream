//go:build windows

package winapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/coral-coder/windstream/internal/config"
	"github.com/coral-coder/windstream/internal/deps"
	"github.com/coral-coder/windstream/internal/netx"
)

// Uninstall implements `--uninstall` (Apps & features). Flags:
// --remove-data / --keep-data; asks when neither is given.
func Uninstall(args []string) error {
	has := func(f string) bool {
		for _, a := range args {
			if a == f {
				return true
			}
		}
		return false
	}
	if !isElevated() {
		self, _ := os.Executable()
		code, err := runElevated(self, strings.Join(append([]string{"--uninstall"}, args...), " "))
		if err == ErrCancelled {
			return nil
		}
		if err == nil && code != 0 {
			return fmt.Errorf("uninstaller exited with code %d", code)
		}
		return err
	}
	removeData := has("--remove-data")
	if !has("--remove-data") && !has("--keep-data") {
		if messageBox("Uninstall Windstream from this PC?", mbOKCancel|mbIconQuestion) != idOK {
			return nil
		}
		removeData = messageBox("Also delete your Windstream accounts and settings?\n\nChoose No to keep them for a later reinstall.", mbYesNo|mbIconQuestion) == idYes
	}
	stopResident(20 * time.Second)
	performUninstall(removeData)
	return nil
}

// performUninstall removes everything Windstream added. It runs inside the
// installed exe (from Apps & features, or from the dashboard after the app
// has shut down); files that are still in use, like this exe, are removed
// by Windows at the next restart.
func performUninstall(removeData bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	netx.RemoveMappings(ctx, mappedPorts())
	cancel()

	if _, err := os.Stat(filepath.Join(DataDir, "vdd.installed")); err == nil {
		_ = deps.UninstallVDD()
		if removeData {
			_ = os.RemoveAll(deps.VDDConfigDir)
		}
	}
	deleteTask()
	removeFirewallRule()
	_ = os.Remove(startMenuLink())
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, uninstallKey)
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, appRegKey)

	pending := removeTree(InstallDir)
	if removeData {
		pending += removeTree(DataDir)
	} else {
		pending += removeTree(filepath.Join(DataDir, "bin"))
		pending += removeTree(filepath.Join(DataDir, "downloads"))
	}
	msg := "Windstream has been removed."
	if !removeData {
		msg += "\n\nYour accounts and settings were kept in " + DataDir + "."
	}
	if pending > 0 {
		msg += "\n\nA few files that were in use will be deleted when you restart the PC."
	}
	messageBox(msg, mbOK|mbIconInfo)
}

// removeTree deletes dir; anything locked is scheduled for deletion at the
// next reboot. It returns how many items were deferred.
func removeTree(dir string) int {
	if err := os.RemoveAll(dir); err == nil {
		return 0
	}
	deferred := 0
	var dirs []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if os.Remove(path) != nil {
			if p, err := windows.UTF16PtrFromString(path); err == nil &&
				windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT) == nil {
				deferred++
			}
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- { // deepest first
		if os.Remove(dirs[i]) != nil {
			if p, err := windows.UTF16PtrFromString(dirs[i]); err == nil &&
				windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT) == nil {
				deferred++
			}
		}
	}
	return deferred
}

// mappedPorts reads the ports Windstream used (they may have been moved off
// the defaults) so their router mappings can be removed.
func mappedPorts() netx.Ports {
	ports := netx.Ports{HTTPSInternal: 8443, HTTPSExternal: 443, MediaUDP: 8444}
	if cfg, err := config.Load(filepath.Join(DataDir, "windstream.toml")); err == nil {
		ports.HTTPSInternal = uint16(cfg.ListenPort())
		ports.HTTPSExternal = uint16(cfg.Network.HTTPSExternalPort)
		ports.MediaUDP = uint16(cfg.WebRTC.UDPPort)
		ports.MediaTCP = uint16(cfg.WebRTC.TCPPort)
	}
	return ports
}

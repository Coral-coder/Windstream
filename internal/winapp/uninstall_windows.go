//go:build windows

package winapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/coral-coder/windstream/internal/config"
	"github.com/coral-coder/windstream/internal/deps"
	"github.com/coral-coder/windstream/internal/netx"
)

const createBreakawayFromJob = 0x01000000

// StartUninstall launches the uninstaller as a separate process from a temp
// copy (so it can delete Program Files\Windstream, including this exe).
func StartUninstall(removeData bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("windstream-uninstall-%d.exe", time.Now().UnixNano()))
	if err := copyFile(self, tmp); err != nil {
		return err
	}
	args := []string{"--uninstall", "--from-temp"}
	if removeData {
		args = append(args, "--remove-data")
	} else {
		args = append(args, "--keep-data")
	}
	// Task Scheduler runs the app inside a job object; break away so the
	// uninstaller survives the app quitting.
	cmd := hidden(tmp, args...)
	cmd.SysProcAttr.CreationFlags |= createBreakawayFromJob
	if err := cmd.Start(); err == nil {
		return nil
	}
	cmd = hidden(tmp, args...)
	return cmd.Start()
}

// Uninstall implements `--uninstall`. Flags: --remove-data / --keep-data
// (asks when neither is given), --from-temp (internal).
func Uninstall(args []string) error {
	has := func(f string) bool {
		for _, a := range args {
			if a == f {
				return true
			}
		}
		return false
	}
	self, _ := os.Executable()
	if !isElevated() {
		code, err := runElevated(self, strings.Join(append([]string{"--uninstall"}, args...), " "))
		if err == ErrCancelled {
			return nil
		}
		if err == nil && code != 0 {
			return fmt.Errorf("uninstaller exited with code %d", code)
		}
		return err
	}
	if !has("--from-temp") {
		removeData := has("--remove-data")
		if !has("--remove-data") && !has("--keep-data") {
			if messageBox("Uninstall Windstream from this PC?", mbOKCancel|mbIconQuestion) != idOK {
				return nil
			}
			removeData = messageBox("Also delete your Windstream accounts and settings?\n\nChoose No to keep them for a later reinstall.", mbYesNo|mbIconQuestion) == idYes
		}
		return StartUninstall(removeData)
	}

	removeData := has("--remove-data")
	stopResident(20 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	netx.RemoveMappings(ctx, mappedPorts())
	cancel()

	if _, err := os.Stat(filepath.Join(DataDir, "vdd.installed")); err == nil {
		_ = deps.UninstallVDD()
		if removeData {
			_ = os.RemoveAll(deps.VDDConfigDir)
		}
	}
	_ = hidden(system32("schtasks.exe"), "/Delete", "/TN", taskName, "/F").Run()
	removeFirewallRule()
	_ = os.Remove(startMenuLink())
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, uninstallKey)
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, appRegKey)
	var lastErr error
	for i := 0; i < 10; i++ {
		if lastErr = os.RemoveAll(InstallDir); lastErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if removeData {
		_ = os.RemoveAll(DataDir)
	} else {
		_ = os.RemoveAll(filepath.Join(DataDir, "bin"))
		_ = os.RemoveAll(filepath.Join(DataDir, "downloads"))
	}
	msg := "Windstream has been removed."
	if !removeData {
		msg += "\n\nYour accounts and settings were kept in " + DataDir + "."
	}
	if lastErr != nil {
		msg += "\n\nSome files could not be deleted: " + lastErr.Error()
	}
	messageBox(msg, mbOK|mbIconInfo)

	// Delete this temporary uninstaller after it exits.
	_ = hiddenCmdLine(system32("cmd.exe"), fmt.Sprintf(`cmd.exe /c ping -n 3 127.0.0.1 >nul & del /f /q "%s"`, self)).Start()
	return nil
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

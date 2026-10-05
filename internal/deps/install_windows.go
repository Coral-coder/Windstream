//go:build windows

package deps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/coral-coder/windstream/internal/input"
)

var guidDevClassDisplay = windows.GUID{Data1: 0x4d36e968, Data2: 0xe325, Data3: 0x11ce, Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}

var (
	newdev                                = windows.NewLazySystemDLL("newdev.dll")
	procUpdateDriverForPlugAndPlayDevices = newdev.NewProc("UpdateDriverForPlugAndPlayDevicesW")
)

const installflagForce = 0x00000001

func (m *Manager) ensureViGEm(ctx context.Context) error {
	if input.ViGEmBusPresent() {
		return nil
	}
	exe := filepath.Join(m.dataDir, "downloads", filepath.Base(ViGEmBus.URL))
	m.log.Info("downloading", "artifact", ViGEmBus.Name)
	if err := Download(ctx, ViGEmBus, exe, m.progressFn("vigem")); err != nil {
		return err
	}
	m.set("vigem", StateInstalling, 1, "installing driver")
	// Advanced Installer bootstrapper: no UI, silent MSI, no reboot.
	cmd := exec.CommandContext(ctx, exe, "/exenoui", "/qn", "/norestart")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 3010 {
		err = nil // installed; reboot recommended
	}
	if err != nil {
		return fmt.Errorf("ViGEmBus installer: %w", err)
	}
	for i := 0; i < 30 && !input.ViGEmBusPresent(); i++ {
		time.Sleep(time.Second)
	}
	if !input.ViGEmBusPresent() {
		return errors.New("ViGEmBus installed but the bus is not running yet; restart the PC")
	}
	m.log.Info("installed", "artifact", ViGEmBus.Name)
	_ = os.WriteFile(filepath.Join(m.dataDir, "vigem.installed"), []byte(ViGEmBus.Name), 0o644)
	return nil
}

// findVDDDevice returns the display-class device whose hardware IDs include
// Root\MttVDD.
func findVDDDevice() (windows.DevInfo, *windows.DevInfoData, bool) {
	set, err := windows.SetupDiGetClassDevsEx(&guidDevClassDisplay, "", 0, 0, 0, "")
	if err != nil {
		return 0, nil, false
	}
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if err != nil {
			continue
		}
		v, err := set.DeviceRegistryProperty(data, windows.SPDRP_HARDWAREID)
		if err != nil {
			continue
		}
		if ids, ok := v.([]string); ok {
			for _, id := range ids {
				if strings.EqualFold(id, VDDHardwareID) {
					return set, data, true
				}
			}
		}
	}
	set.Close()
	return 0, nil, false
}

func (m *Manager) ensureVDD(ctx context.Context) error {
	if _, err := WriteVDDSettings(VDDConfigDir, m.VDDWidth, m.VDDHeight, m.VDDRefresh); err != nil {
		return fmt.Errorf("write driver settings: %w", err)
	}
	if set, _, ok := findVDDDevice(); ok {
		set.Close()
		return nil
	}
	zipPath := filepath.Join(m.dataDir, "downloads", filepath.Base(VirtualDisplayDriver.URL))
	m.log.Info("downloading", "artifact", VirtualDisplayDriver.Name)
	if err := Download(ctx, VirtualDisplayDriver, zipPath, m.progressFn("vdd")); err != nil {
		return err
	}
	m.set("vdd", StateInstalling, 1, "installing driver")
	dir := filepath.Join(m.dataDir, "drivers", "vdd")
	if _, err := ExtractMatching(zipPath, "VirtualDisplayDriver/", dir); err != nil {
		return err
	}
	inf := filepath.Join(dir, "MttVDD.inf")

	// Equivalent of `devcon install MttVDD.inf Root\MttVDD`: create the root
	// device node, then bind the driver to it.
	set, err := windows.SetupDiCreateDeviceInfoListEx(&guidDevClassDisplay, 0, "")
	if err != nil {
		return fmt.Errorf("SetupDiCreateDeviceInfoList: %w", err)
	}
	defer set.Close()
	data, err := set.CreateDeviceInfo("Display", &guidDevClassDisplay, "", 0, windows.DICD_GENERATE_ID)
	if err != nil {
		return fmt.Errorf("SetupDiCreateDeviceInfo: %w", err)
	}
	hwid := windows.StringToUTF16(VDDHardwareID + "\x00") // REG_MULTI_SZ: double NUL
	hwidBytes := unsafe.Slice((*byte)(unsafe.Pointer(&hwid[0])), len(hwid)*2)
	if err := set.SetDeviceRegistryProperty(data, windows.SPDRP_HARDWAREID, hwidBytes); err != nil {
		return fmt.Errorf("set hardware ID: %w", err)
	}
	if err := set.CallClassInstaller(windows.DIF_REGISTERDEVICE, data); err != nil {
		return fmt.Errorf("register device: %w", err)
	}
	hw, _ := windows.UTF16PtrFromString(VDDHardwareID)
	infW, _ := windows.UTF16PtrFromString(inf)
	var reboot int32
	r, _, callErr := procUpdateDriverForPlugAndPlayDevices.Call(0, uintptr(unsafe.Pointer(hw)), uintptr(unsafe.Pointer(infW)),
		installflagForce, uintptr(unsafe.Pointer(&reboot)))
	if r == 0 {
		_ = set.CallClassInstaller(windows.DIF_REMOVE, data)
		return fmt.Errorf("install display driver: %v", callErr)
	}
	m.log.Info("installed", "artifact", VirtualDisplayDriver.Name, "reboot_required", reboot != 0)
	_ = os.WriteFile(filepath.Join(m.dataDir, "vdd.installed"), []byte(VirtualDisplayDriver.Name), 0o644)
	return nil
}

// UninstallVDD removes the virtual display device node.
func UninstallVDD() error {
	set, data, ok := findVDDDevice()
	if !ok {
		return nil
	}
	defer set.Close()
	return set.CallClassInstaller(windows.DIF_REMOVE, data)
}

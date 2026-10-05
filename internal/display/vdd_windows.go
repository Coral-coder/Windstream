//go:build windows

package display

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// guidDevClassDisplay is GUID_DEVCLASS_DISPLAY.
var guidDevClassDisplay = windows.GUID{Data1: 0x4d36e968, Data2: 0xe325, Data3: 0x11ce, Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}

const cmProbDisabled = 22

type vddDevice struct {
	set     windows.DevInfo
	data    *windows.DevInfoData
	name    string
	enabled bool
}

func (d *vddDevice) close() { _ = d.set.Close() }

// findVDD locates the virtual display adapter in Device Manager by name.
func findVDD(substr string) (*vddDevice, error) {
	set, err := windows.SetupDiGetClassDevsEx(&guidDevClassDisplay, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return nil, fmt.Errorf("SetupDiGetClassDevs: %w", err)
	}
	want := strings.ToLower(substr)
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if err != nil {
			continue
		}
		var name string
		for _, prop := range []windows.SPDRP{windows.SPDRP_FRIENDLYNAME, windows.SPDRP_DEVICEDESC} {
			if v, err := set.DeviceRegistryProperty(data, prop); err == nil {
				if s, ok := v.(string); ok && s != "" {
					name = s
					break
				}
			}
		}
		if !strings.Contains(strings.ToLower(name), want) {
			continue
		}
		var status, problem uint32
		enabled := true
		if err := windows.CM_Get_DevNode_Status(&status, &problem, data.DevInst, 0); err == nil {
			enabled = !(status&windows.DN_HAS_PROBLEM != 0 && problem == cmProbDisabled)
		}
		return &vddDevice{set: set, data: data, name: name, enabled: enabled}, nil
	}
	set.Close()
	return nil, fmt.Errorf("no display adapter matching %q found in Device Manager; install the Virtual Display Driver (see docs/windows-setup.md) or set display.virtual_device", substr)
}

// setEnabled enables or disables the device (requires Administrator).
func (d *vddDevice) setEnabled(on bool) error {
	params := windows.PropChangeParams{
		ClassInstallHeader: *windows.MakeClassInstallHeader(windows.DIF_PROPERTYCHANGE),
		StateChange:        windows.DICS_DISABLE,
		Scope:              windows.DICS_FLAG_GLOBAL,
	}
	if on {
		params.StateChange = windows.DICS_ENABLE
	}
	if err := d.set.SetClassInstallParams(d.data, &params.ClassInstallHeader, uint32(unsafe.Sizeof(params))); err != nil {
		return fmt.Errorf("SetupDiSetClassInstallParams: %w", err)
	}
	if err := d.set.CallClassInstaller(windows.DIF_PROPERTYCHANGE, d.data); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return fmt.Errorf("toggling %q needs Administrator rights (run elevated, see docs/windows-setup.md): %w", d.name, err)
		}
		return fmt.Errorf("SetupDiCallClassInstaller: %w", err)
	}
	d.enabled = on
	return nil
}

// VirtualDeviceStatus reports whether the virtual display adapter exists and
// is enabled, for `windstream check`.
func VirtualDeviceStatus(substr string) (string, error) {
	d, err := findVDD(substr)
	if err != nil {
		return "", err
	}
	defer d.close()
	state := "disabled (Windstream enables it at start)"
	if d.enabled {
		state = "enabled"
	}
	return fmt.Sprintf("%s: %s", d.name, state), nil
}

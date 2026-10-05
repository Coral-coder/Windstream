//go:build windows

package display

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procEnumDisplayDevicesW      = user32.NewProc("EnumDisplayDevicesW")
	procEnumDisplaySettingsExW   = user32.NewProc("EnumDisplaySettingsExW")
	procChangeDisplaySettingsExW = user32.NewProc("ChangeDisplaySettingsExW")

	dxgi                   = windows.NewLazySystemDLL("dxgi.dll")
	procCreateDXGIFactory1 = dxgi.NewProc("CreateDXGIFactory1")
	iidIDXGIFactory1       = windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	dxgiErrorNotFound      = uintptr(0x887A0002)
)

const (
	displayDeviceAttachedToDesktop = 0x1
	displayDevicePrimary           = 0x4

	enumCurrentSettings = 0xFFFFFFFF

	dmPosition         = 0x00000020
	dmBitsPerPel       = 0x00040000
	dmPelsWidth        = 0x00080000
	dmPelsHeight       = 0x00100000
	dmDisplayFrequency = 0x00400000

	cdsUpdateRegistry = 0x00000001
	cdsSetPrimary     = 0x00000010
	cdsNoReset        = 0x10000000

	dispChangeSuccessful = 0
)

// displayDevice mirrors DISPLAY_DEVICEW.
type displayDevice struct {
	cb           uint32
	DeviceName   [32]uint16
	DeviceString [128]uint16
	StateFlags   uint32
	DeviceID     [128]uint16
	DeviceKey    [128]uint16
}

// devMode mirrors DEVMODEW (display variant of the unions). 220 bytes.
type devMode struct {
	DeviceName         [32]uint16
	SpecVersion        uint16
	DriverVersion      uint16
	Size               uint16
	DriverExtra        uint16
	Fields             uint32
	PositionX          int32
	PositionY          int32
	DisplayOrientation uint32
	DisplayFixedOutput uint32
	Color              int16
	Duplex             int16
	YResolution        int16
	TTOption           int16
	Collate            int16
	FormName           [32]uint16
	LogPixels          uint16
	BitsPerPel         uint32
	PelsWidth          uint32
	PelsHeight         uint32
	DisplayFlags       uint32
	DisplayFrequency   uint32
	ICMMethod          uint32
	ICMIntent          uint32
	MediaType          uint32
	DitherType         uint32
	Reserved1          uint32
	Reserved2          uint32
	PanningWidth       uint32
	PanningHeight      uint32
}

var _ [220]byte = [unsafe.Sizeof(devMode{})]byte{}

type gdiDisplay struct {
	name, description string
	attached, primary bool
}

func enumGDIDisplays() []gdiDisplay {
	var out []gdiDisplay
	for i := uint32(0); i < 64; i++ {
		var dd displayDevice
		dd.cb = uint32(unsafe.Sizeof(dd))
		r, _, _ := procEnumDisplayDevicesW.Call(0, uintptr(i), uintptr(unsafe.Pointer(&dd)), 0)
		if r == 0 {
			break
		}
		out = append(out, gdiDisplay{
			name:        windows.UTF16ToString(dd.DeviceName[:]),
			description: windows.UTF16ToString(dd.DeviceString[:]),
			attached:    dd.StateFlags&displayDeviceAttachedToDesktop != 0,
			primary:     dd.StateFlags&displayDevicePrimary != 0,
		})
	}
	return out
}

func currentMode(device string) (devMode, error) {
	var dm devMode
	dm.Size = uint16(unsafe.Sizeof(dm))
	name, err := windows.UTF16PtrFromString(device)
	if err != nil {
		return dm, err
	}
	r, _, _ := procEnumDisplaySettingsExW.Call(uintptr(unsafe.Pointer(name)), enumCurrentSettings, uintptr(unsafe.Pointer(&dm)), 0)
	if r == 0 {
		return dm, fmt.Errorf("EnumDisplaySettingsEx(%s) failed", device)
	}
	return dm, nil
}

func changeSettings(device string, dm *devMode, flags uint32) error {
	var namePtr uintptr
	if device != "" {
		name, err := windows.UTF16PtrFromString(device)
		if err != nil {
			return err
		}
		namePtr = uintptr(unsafe.Pointer(name))
	}
	var dmPtr uintptr
	if dm != nil {
		dm.Size = uint16(unsafe.Sizeof(*dm))
		dmPtr = uintptr(unsafe.Pointer(dm))
	}
	r, _, _ := procChangeDisplaySettingsExW.Call(namePtr, dmPtr, 0, uintptr(flags), 0)
	if int32(r) != dispChangeSuccessful {
		return fmt.Errorf("ChangeDisplaySettingsEx(%q) returned %d", device, int32(r))
	}
	return nil
}

// setMode applies a resolution/refresh (and attaches the display to the
// desktop if it is not yet part of it).
func setMode(device string, width, height, refresh int, x, y int) error {
	var dm devMode
	dm.Fields = dmPelsWidth | dmPelsHeight | dmDisplayFrequency | dmBitsPerPel | dmPosition
	dm.PelsWidth, dm.PelsHeight = uint32(width), uint32(height)
	dm.DisplayFrequency = uint32(refresh)
	dm.BitsPerPel = 32
	dm.PositionX, dm.PositionY = int32(x), int32(y)
	return changeSettings(device, &dm, cdsUpdateRegistry)
}

// makePrimary moves device to (0,0), shifting every other display by the same
// offset so the physical arrangement is preserved, then applies atomically.
func makePrimary(device string, outputs []Output) error {
	pos := Translate(outputs, device)
	for _, o := range outputs {
		p, ok := pos[o.DeviceName]
		if !ok {
			continue
		}
		var dm devMode
		dm.Fields = dmPosition
		dm.PositionX, dm.PositionY = int32(p[0]), int32(p[1])
		flags := uint32(cdsUpdateRegistry | cdsNoReset)
		if o.DeviceName == device {
			flags |= cdsSetPrimary
		}
		if err := changeSettings(o.DeviceName, &dm, flags); err != nil {
			return err
		}
	}
	return changeSettings("", nil, 0) // apply the staged changes
}

// ---- DXGI enumeration via raw COM vtable calls ----

type comObj struct{ vtbl *[64]uintptr }

func (o *comObj) call(idx int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[idx], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return r
}

func (o *comObj) release() { o.call(2) }

const (
	vtFactoryEnumAdapters1 = 12 // IUnknown(3) + IDXGIObject(4) + IDXGIFactory(5)
	vtAdapterEnumOutputs   = 7  // IUnknown(3) + IDXGIObject(4)
	vtAdapterGetDesc1      = 10 // + EnumOutputs, GetDesc, CheckInterfaceSupport
	vtOutputGetDesc        = 7
)

type dxgiAdapterDesc1 struct {
	Description           [128]uint16
	VendorID              uint32
	DeviceID              uint32
	SubSysID              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	LuidLow               uint32
	LuidHigh              int32
	Flags                 uint32
}

type dxgiOutputDesc struct {
	DeviceName        [32]uint16
	Left, Top         int32
	Right, Bottom     int32
	AttachedToDesktop int32
	Rotation          uint32
	Monitor           uintptr
}

// List enumerates every desktop-attached output across all GPUs.
func List() ([]Output, error) {
	var factory *comObj
	if hr, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); hr != 0 {
		return nil, fmt.Errorf("CreateDXGIFactory1: hresult %#x", hr)
	}
	defer factory.release()

	gdi := map[string]gdiDisplay{}
	for _, d := range enumGDIDisplays() {
		gdi[d.name] = d
	}
	var outs []Output
	for a := 0; ; a++ {
		var adapter *comObj
		hr := factory.call(vtFactoryEnumAdapters1, uintptr(a), uintptr(unsafe.Pointer(&adapter)))
		if hr == dxgiErrorNotFound {
			break
		}
		if hr != 0 {
			return nil, fmt.Errorf("EnumAdapters1(%d): hresult %#x", a, hr)
		}
		var ad dxgiAdapterDesc1
		adapter.call(vtAdapterGetDesc1, uintptr(unsafe.Pointer(&ad)))
		adapterName := windows.UTF16ToString(ad.Description[:])
		for o := 0; ; o++ {
			var output *comObj
			hr := adapter.call(vtAdapterEnumOutputs, uintptr(o), uintptr(unsafe.Pointer(&output)))
			if hr == dxgiErrorNotFound {
				break
			}
			if hr != 0 {
				break
			}
			var od dxgiOutputDesc
			output.call(vtOutputGetDesc, uintptr(unsafe.Pointer(&od)))
			output.release()
			if od.AttachedToDesktop == 0 {
				continue
			}
			name := windows.UTF16ToString(od.DeviceName[:])
			g := gdi[name]
			outs = append(outs, Output{
				Index: len(outs), Adapter: a, AdapterName: adapterName, Output: o,
				DeviceName: name, MonitorName: g.description,
				Width: int(od.Right - od.Left), Height: int(od.Bottom - od.Top),
				X: int(od.Left), Y: int(od.Top), Primary: g.primary,
			})
		}
		adapter.release()
	}
	return outs, nil
}

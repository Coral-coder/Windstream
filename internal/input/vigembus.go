package input

import (
	"bytes"
	"encoding/binary"
)

// ViGEmBus wire protocol, ported from ViGEmClient's km/BusShared.h so no
// client DLL is needed. All requests are METHOD_BUFFERED IOCTLs on the bus
// device interface {96E42B22-F5E9-42F8-B043-ED0F932F014F}.

const (
	fileDeviceBusExtender = 0x2A
	fileReadData          = 1
	fileWriteData         = 2
	vigemIOCTLBase        = 0x801

	vigemCommonVersion = 0x0001
	vigemTargetsMax    = 16

	targetXbox360Wired = 0
	x360VendorID       = 0x045E
	x360ProductID      = 0x028E
)

func ctlCode(deviceType, function, method, access uint32) uint32 {
	return deviceType<<16 | access<<14 | function<<2 | method
}

var (
	ioctlPluginTarget    = ctlCode(fileDeviceBusExtender, vigemIOCTLBase+0x000, 0, fileWriteData)
	ioctlUnplugTarget    = ctlCode(fileDeviceBusExtender, vigemIOCTLBase+0x001, 0, fileWriteData)
	ioctlCheckVersion    = ctlCode(fileDeviceBusExtender, vigemIOCTLBase+0x002, 0, fileWriteData)
	ioctlWaitDeviceReady = ctlCode(fileDeviceBusExtender, vigemIOCTLBase+0x003, 0, fileWriteData)
	ioctlXUSBSubmit      = ctlCode(fileDeviceBusExtender, vigemIOCTLBase+0x201, 0, fileWriteData)
)

func le(fields ...any) []byte {
	var b bytes.Buffer
	for _, f := range fields {
		_ = binary.Write(&b, binary.LittleEndian, f)
	}
	return b.Bytes()
}

// VIGEM_CHECK_VERSION { ULONG Size; ULONG Version; }
func checkVersionMsg() []byte { return le(uint32(8), uint32(vigemCommonVersion)) }

// VIGEM_PLUGIN_TARGET { ULONG Size; ULONG SerialNo; VIGEM_TARGET_TYPE TargetType; USHORT VendorId; USHORT ProductId; }
func pluginMsg(serial uint32) []byte {
	return le(uint32(16), serial, uint32(targetXbox360Wired), uint16(x360VendorID), uint16(x360ProductID))
}

// VIGEM_UNPLUG_TARGET / VIGEM_WAIT_DEVICE_READY { ULONG Size; ULONG SerialNo; }
func serialMsg(serial uint32) []byte { return le(uint32(8), serial) }

// XUSB_SUBMIT_REPORT { ULONG Size; ULONG SerialNo; XUSB_REPORT Report; }
func xusbSubmitMsg(serial uint32, r xusbReport) []byte {
	return le(uint32(20), serial, r)
}

package input

import "github.com/coral-coder/windstream/internal/protocol"

// XUSB (XInput) button bits.
const (
	xusbDPadUp        = 0x0001
	xusbDPadDown      = 0x0002
	xusbDPadLeft      = 0x0004
	xusbDPadRight     = 0x0008
	xusbStart         = 0x0010
	xusbBack          = 0x0020
	xusbLeftThumb     = 0x0040
	xusbRightThumb    = 0x0080
	xusbLeftShoulder  = 0x0100
	xusbRightShoulder = 0x0200
	xusbGuide         = 0x0400
	xusbA             = 0x1000
	xusbB             = 0x2000
	xusbX             = 0x4000
	xusbY             = 0x8000
)

// xusbReport mirrors ViGEm's XUSB_REPORT (12 bytes).
type xusbReport struct {
	Buttons      uint16
	LeftTrigger  uint8
	RightTrigger uint8
	ThumbLX      int16
	ThumbLY      int16
	ThumbRX      int16
	ThumbRY      int16
}

var standardToXUSB = [...]struct {
	btn  int
	mask uint16
}{
	{protocol.BtnA, xusbA}, {protocol.BtnB, xusbB}, {protocol.BtnX, xusbX}, {protocol.BtnY, xusbY},
	{protocol.BtnLB, xusbLeftShoulder}, {protocol.BtnRB, xusbRightShoulder},
	{protocol.BtnBack, xusbBack}, {protocol.BtnStart, xusbStart},
	{protocol.BtnLS, xusbLeftThumb}, {protocol.BtnRS, xusbRightThumb},
	{protocol.BtnDUp, xusbDPadUp}, {protocol.BtnDDown, xusbDPadDown},
	{protocol.BtnDLeft, xusbDPadLeft}, {protocol.BtnDRight, xusbDPadRight},
	{protocol.BtnGuide, xusbGuide},
}

// toXUSB converts a W3C standard-mapping snapshot to an Xbox 360 report.
// Browsers report stick Y with down positive; XInput uses up positive.
func toXUSB(st protocol.GamepadState) xusbReport {
	var r xusbReport
	for _, m := range standardToXUSB {
		if st.Pressed(m.btn) {
			r.Buttons |= m.mask
		}
	}
	r.LeftTrigger, r.RightTrigger = st.Triggers[0], st.Triggers[1]
	r.ThumbLX = st.Axes[0]
	r.ThumbLY = invertAxis(st.Axes[1])
	r.ThumbRX = st.Axes[2]
	r.ThumbRY = invertAxis(st.Axes[3])
	return r
}

func invertAxis(v int16) int16 {
	if v == -32768 {
		return 32767
	}
	return -v
}

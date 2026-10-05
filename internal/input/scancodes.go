package input

// scanCode is a PS/2 set-1 scan code. Games that read DirectInput or Raw Input
// only see real scan codes, so keyboard input is injected by scan code rather
// than by virtual-key.
type scanCode struct {
	code     uint16
	extended bool // E0-prefixed key
}

// scanCodes maps KeyboardEvent.code values to scan codes.
var scanCodes = map[string]scanCode{
	"Escape": {0x01, false}, "Digit1": {0x02, false}, "Digit2": {0x03, false}, "Digit3": {0x04, false},
	"Digit4": {0x05, false}, "Digit5": {0x06, false}, "Digit6": {0x07, false}, "Digit7": {0x08, false},
	"Digit8": {0x09, false}, "Digit9": {0x0A, false}, "Digit0": {0x0B, false},
	"Minus": {0x0C, false}, "Equal": {0x0D, false}, "Backspace": {0x0E, false}, "Tab": {0x0F, false},
	"KeyQ": {0x10, false}, "KeyW": {0x11, false}, "KeyE": {0x12, false}, "KeyR": {0x13, false},
	"KeyT": {0x14, false}, "KeyY": {0x15, false}, "KeyU": {0x16, false}, "KeyI": {0x17, false},
	"KeyO": {0x18, false}, "KeyP": {0x19, false}, "BracketLeft": {0x1A, false}, "BracketRight": {0x1B, false},
	"Enter": {0x1C, false}, "ControlLeft": {0x1D, false},
	"KeyA": {0x1E, false}, "KeyS": {0x1F, false}, "KeyD": {0x20, false}, "KeyF": {0x21, false},
	"KeyG": {0x22, false}, "KeyH": {0x23, false}, "KeyJ": {0x24, false}, "KeyK": {0x25, false},
	"KeyL": {0x26, false}, "Semicolon": {0x27, false}, "Quote": {0x28, false}, "Backquote": {0x29, false},
	"ShiftLeft": {0x2A, false}, "Backslash": {0x2B, false},
	"KeyZ": {0x2C, false}, "KeyX": {0x2D, false}, "KeyC": {0x2E, false}, "KeyV": {0x2F, false},
	"KeyB": {0x30, false}, "KeyN": {0x31, false}, "KeyM": {0x32, false},
	"Comma": {0x33, false}, "Period": {0x34, false}, "Slash": {0x35, false}, "ShiftRight": {0x36, false},
	"NumpadMultiply": {0x37, false}, "AltLeft": {0x38, false}, "Space": {0x39, false}, "CapsLock": {0x3A, false},
	"F1": {0x3B, false}, "F2": {0x3C, false}, "F3": {0x3D, false}, "F4": {0x3E, false}, "F5": {0x3F, false},
	"F6": {0x40, false}, "F7": {0x41, false}, "F8": {0x42, false}, "F9": {0x43, false}, "F10": {0x44, false},
	"NumLock": {0x45, false}, "ScrollLock": {0x46, false},
	"Numpad7": {0x47, false}, "Numpad8": {0x48, false}, "Numpad9": {0x49, false}, "NumpadSubtract": {0x4A, false},
	"Numpad4": {0x4B, false}, "Numpad5": {0x4C, false}, "Numpad6": {0x4D, false}, "NumpadAdd": {0x4E, false},
	"Numpad1": {0x4F, false}, "Numpad2": {0x50, false}, "Numpad3": {0x51, false},
	"Numpad0": {0x52, false}, "NumpadDecimal": {0x53, false},
	"IntlBackslash": {0x56, false}, "F11": {0x57, false}, "F12": {0x58, false},
	"F13": {0x64, false}, "F14": {0x65, false}, "F15": {0x66, false}, "F16": {0x67, false},
	"F17": {0x68, false}, "F18": {0x69, false}, "F19": {0x6A, false}, "F20": {0x6B, false},
	"F21": {0x6C, false}, "F22": {0x6D, false}, "F23": {0x6E, false}, "F24": {0x76, false},
	"KanaMode": {0x70, false}, "IntlRo": {0x73, false}, "Convert": {0x79, false},
	"NonConvert": {0x7B, false}, "IntlYen": {0x7D, false},

	"NumpadEnter": {0x1C, true}, "ControlRight": {0x1D, true}, "NumpadDivide": {0x35, true},
	"PrintScreen": {0x37, true}, "AltRight": {0x38, true},
	"Home": {0x47, true}, "ArrowUp": {0x48, true}, "PageUp": {0x49, true},
	"ArrowLeft": {0x4B, true}, "ArrowRight": {0x4D, true}, "End": {0x4F, true},
	"ArrowDown": {0x50, true}, "PageDown": {0x51, true}, "Insert": {0x52, true}, "Delete": {0x53, true},
	"MetaLeft": {0x5B, true}, "MetaRight": {0x5C, true}, "ContextMenu": {0x5D, true},
	"AudioVolumeMute": {0x20, true}, "AudioVolumeDown": {0x2E, true}, "AudioVolumeUp": {0x30, true},
	"MediaPlayPause": {0x22, true}, "MediaStop": {0x24, true},
	"MediaTrackPrevious": {0x10, true}, "MediaTrackNext": {0x19, true},
}

// Keys with no usable scan code are sent by virtual-key code instead.
var virtualKeys = map[string]uint16{
	"Pause": 0x13, // VK_PAUSE (scan code is the multi-byte E1 sequence)
}

// ScanCode resolves a KeyboardEvent.code.
func ScanCode(code string) (sc uint16, extended, ok bool) {
	s, ok := scanCodes[code]
	return s.code, s.extended, ok
}

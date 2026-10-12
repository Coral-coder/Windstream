//go:build windows

package power

import (
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	user32                 = windows.NewLazySystemDLL("user32.dll")
	procPowerCreateRequest = kernel32.NewProc("PowerCreateRequest")
	procPowerSetRequest    = kernel32.NewProc("PowerSetRequest")
	procPowerClearRequest  = kernel32.NewProc("PowerClearRequest")
	procSetThreadExecState = kernel32.NewProc("SetThreadExecutionState")
	procSendInput          = user32.NewProc("SendInput")
)

const (
	powerRequestContextSimpleString = 0x1
	powerRequestDisplayRequired     = 0
	powerRequestSystemRequired      = 1

	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002

	inputMouse      = 0
	mouseeventfMove = 0x0001
)

// reasonContext mirrors REASON_CONTEXT (simple-string form) on 64-bit.
type reasonContext struct {
	version uint32
	flags   uint32
	reason  *uint16
	_       [16]byte // rest of the union
}

var reasonText, _ = windows.UTF16PtrFromString("Windstream is streaming this PC")

// KeepAwake stops the display and the PC from going to sleep until the
// returned function is called.
func KeepAwake() (release func()) {
	ctx := reasonContext{flags: powerRequestContextSimpleString, reason: reasonText}
	h, _, _ := procPowerCreateRequest.Call(uintptr(unsafe.Pointer(&ctx)))
	if h == 0 || windows.Handle(h) == windows.InvalidHandle {
		return func() {}
	}
	procPowerSetRequest.Call(h, powerRequestDisplayRequired)
	procPowerSetRequest.Call(h, powerRequestSystemRequired)
	var once sync.Once
	return func() {
		once.Do(func() {
			procPowerClearRequest.Call(h, powerRequestDisplayRequired)
			procPowerClearRequest.Call(h, powerRequestSystemRequired)
			windows.CloseHandle(windows.Handle(h))
		})
	}
}

// mouseInput / rawInput mirror MOUSEINPUT / INPUT on 64-bit.
type mouseInput struct {
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	extraInfo uintptr
}

type rawInput struct {
	typ   uint32
	_     uint32
	mouse mouseInput
}

var (
	wakeMu   sync.Mutex
	lastWake time.Time
)

// WakeDisplay turns a sleeping display back on. Windows only wakes it for
// input, so this nudges the mouse one pixel and back (nothing visible
// happens), and resets the idle timer. Calls closer than a second apart
// are folded into one.
func WakeDisplay() {
	wakeMu.Lock()
	if time.Since(lastWake) < time.Second {
		wakeMu.Unlock()
		return
	}
	lastWake = time.Now()
	wakeMu.Unlock()
	procSetThreadExecState.Call(esDisplayRequired | esSystemRequired)
	in := []rawInput{
		{typ: inputMouse, mouse: mouseInput{dx: 1, flags: mouseeventfMove}},
		{typ: inputMouse, mouse: mouseInput{dx: -1, flags: mouseeventfMove}},
	}
	// Fails harmlessly on the lock screen or a UAC prompt.
	procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
}

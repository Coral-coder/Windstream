//go:build windows

package input

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
)

var guidViGEmBus = windows.GUID{Data1: 0x96E42B22, Data2: 0xF5E9, Data3: 0x42F8, Data4: [8]byte{0xB0, 0x43, 0xED, 0x0F, 0x93, 0x2F, 0x01, 0x4F}}

// ErrNoViGEmBus means the ViGEmBus driver is not installed.
var ErrNoViGEmBus = errors.New("ViGEmBus driver not installed")

// ViGEmBusPresent reports whether the bus driver is installed and running.
func ViGEmBusPresent() bool {
	paths, err := windows.CM_Get_Device_Interface_List("", &guidViGEmBus, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	return err == nil && len(paths) > 0
}

// vigemClient is an open handle to the ViGEmBus driver.
type vigemClient struct {
	h    windows.Handle
	mu   sync.Mutex
	used [vigemTargetsMax + 1]bool
}

func newViGEmClient() (*vigemClient, error) {
	paths, err := windows.CM_Get_Device_Interface_List("", &guidViGEmBus, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil || len(paths) == 0 {
		return nil, ErrNoViGEmBus
	}
	var lastErr error
	for _, p := range paths {
		name, err := windows.UTF16PtrFromString(p)
		if err != nil {
			continue
		}
		h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			lastErr = err
			continue
		}
		c := &vigemClient{h: h}
		if err := c.ioctl(ioctlCheckVersion, checkVersionMsg()); err != nil {
			windows.CloseHandle(h)
			lastErr = fmt.Errorf("ViGEmBus version check: %w", err)
			continue
		}
		return c, nil
	}
	return nil, fmt.Errorf("open ViGEmBus: %w", lastErr)
}

func (c *vigemClient) ioctl(code uint32, in []byte) error {
	var n uint32
	return windows.DeviceIoControl(c.h, code, &in[0], uint32(len(in)), nil, 0, &n, nil)
}

func (c *vigemClient) close() { windows.CloseHandle(c.h) }

type x360Pad struct {
	c      *vigemClient
	serial uint32
}

// addX360 plugs in a virtual Xbox 360 controller on the first free serial.
func (c *vigemClient) addX360() (*x360Pad, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lastErr error
	for s := uint32(1); s <= vigemTargetsMax; s++ {
		if c.used[s] {
			continue
		}
		if err := c.ioctl(ioctlPluginTarget, pluginMsg(s)); err != nil {
			lastErr = err // serial taken by another ViGEm client; try the next
			continue
		}
		// Blocks until the child device can accept reports (bus v1.17+).
		_ = c.ioctl(ioctlWaitDeviceReady, serialMsg(s))
		c.used[s] = true
		return &x360Pad{c: c, serial: s}, nil
	}
	return nil, fmt.Errorf("no free ViGEm slot: %v", lastErr)
}

func (p *x360Pad) update(r xusbReport) error {
	return p.c.ioctl(ioctlXUSBSubmit, xusbSubmitMsg(p.serial, r))
}

func (p *x360Pad) remove() {
	_ = p.c.ioctl(ioctlUnplugTarget, serialMsg(p.serial))
	p.c.mu.Lock()
	p.c.used[p.serial] = false
	p.c.mu.Unlock()
}

//go:build windows

package input

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ViGEmClient.dll API (https://github.com/nefarius/ViGEmClient).
const vigemErrorNone = 0x20000000

type vigemAPI struct {
	alloc, free, connect, disconnect          *windows.Proc
	targetX360Alloc, targetFree               *windows.Proc
	targetAdd, targetRemove, targetX360Update *windows.Proc
}

var (
	vigemOnce sync.Once
	vigemLib  *vigemAPI
	vigemErr  error
)

func loadViGEm(path string) (*vigemAPI, error) {
	vigemOnce.Do(func() {
		if path == "" {
			exe, err := os.Executable()
			if err != nil {
				vigemErr = err
				return
			}
			path = filepath.Join(filepath.Dir(exe), "ViGEmClient.dll")
		}
		dll, err := windows.LoadDLL(path)
		if err != nil {
			vigemErr = fmt.Errorf("load %s: %w (install the ViGEmBus driver and place ViGEmClient.dll next to windstream.exe)", path, err)
			return
		}
		api := &vigemAPI{}
		procs := map[string]**windows.Proc{
			"vigem_alloc": &api.alloc, "vigem_free": &api.free,
			"vigem_connect": &api.connect, "vigem_disconnect": &api.disconnect,
			"vigem_target_x360_alloc": &api.targetX360Alloc, "vigem_target_free": &api.targetFree,
			"vigem_target_add": &api.targetAdd, "vigem_target_remove": &api.targetRemove,
			"vigem_target_x360_update": &api.targetX360Update,
		}
		for name, dst := range procs {
			p, err := dll.FindProc(name)
			if err != nil {
				vigemErr = fmt.Errorf("%s: %w", path, err)
				return
			}
			*dst = p
		}
		vigemLib = api
	})
	return vigemLib, vigemErr
}

// vigemClient is one connection to the ViGEmBus driver.
type vigemClient struct {
	api    *vigemAPI
	client uintptr
}

func newViGEmClient(dllPath string) (*vigemClient, error) {
	api, err := loadViGEm(dllPath)
	if err != nil {
		return nil, err
	}
	c, _, _ := api.alloc.Call()
	if c == 0 {
		return nil, errors.New("vigem_alloc failed")
	}
	if rc, _, _ := api.connect.Call(c); rc != vigemErrorNone {
		api.free.Call(c)
		return nil, fmt.Errorf("vigem_connect: error %#x (is the ViGEmBus driver installed?)", rc)
	}
	return &vigemClient{api: api, client: c}, nil
}

func (v *vigemClient) close() {
	v.api.disconnect.Call(v.client)
	v.api.free.Call(v.client)
}

type x360Pad struct {
	v      *vigemClient
	target uintptr
}

func (v *vigemClient) addX360() (*x360Pad, error) {
	t, _, _ := v.api.targetX360Alloc.Call()
	if t == 0 {
		return nil, errors.New("vigem_target_x360_alloc failed")
	}
	if rc, _, _ := v.api.targetAdd.Call(v.client, t); rc != vigemErrorNone {
		v.api.targetFree.Call(t)
		return nil, fmt.Errorf("vigem_target_add: error %#x", rc)
	}
	return &x360Pad{v: v, target: t}, nil
}

func (p *x360Pad) update(r xusbReport) error {
	// XUSB_REPORT is 12 bytes, so the Windows x64 ABI passes it by reference
	// to a caller-owned copy.
	rc, _, _ := p.v.api.targetX360Update.Call(p.v.client, p.target, uintptr(unsafe.Pointer(&r)))
	if rc != vigemErrorNone {
		return fmt.Errorf("vigem_target_x360_update: error %#x", rc)
	}
	return nil
}

func (p *x360Pad) remove() {
	p.v.api.targetRemove.Call(p.v.client, p.target)
	p.v.api.targetFree.Call(p.target)
}

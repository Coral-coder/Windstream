//go:build windows

package winapp

import (
	"errors"
	"runtime"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// withCOM runs fn on a locked OS thread with COM initialised.
func withCOM(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		var oe *ole.OleError
		if !errors.As(err, &oe) || oe.Code() != 1 { // S_FALSE: already initialised
			return err
		}
	}
	defer ole.CoUninitialize()
	return fn()
}

// newDispatch creates a COM automation object by ProgID.
func newDispatch(progID string) (*ole.IDispatch, error) {
	unk, err := oleutil.CreateObject(progID)
	if err != nil {
		return nil, err
	}
	defer unk.Release()
	return unk.QueryInterface(ole.IID_IDispatch)
}

func call(d *ole.IDispatch, method string, args ...interface{}) (*ole.IDispatch, error) {
	v, err := oleutil.CallMethod(d, method, args...)
	if err != nil {
		return nil, err
	}
	if v.VT == ole.VT_DISPATCH {
		return v.ToIDispatch(), nil
	}
	_ = v.Clear()
	return nil, nil
}

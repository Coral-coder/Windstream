//go:build darwin

package macos

import (
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

// Point and Rect mirror CGPoint and CGRect (global display coordinates in
// points, origin at the top-left of the main display).
type Point struct{ X, Y float64 }

// Rect is a CGRect.
type Rect struct {
	Origin Point
	W, H   float64
}

// CoreGraphics and friends, loaded on first use.
var (
	loadOnce sync.Once
	loadErr  error

	cgGetActiveDisplayList func(max uint32, displays *uint32, count *uint32) int32
	cgDisplayBounds        func(display uint32) Rect
	cgMainDisplayID        func() uint32
	cgDisplayIsBuiltin     func(display uint32) uint32
	cgDisplayPixelsWide    func(display uint32) uintptr
	cgDisplayPixelsHigh    func(display uint32) uintptr

	cgEventSourceCreate          func(state int32) uintptr
	cgEventCreate                func(source uintptr) uintptr
	cgEventGetLocation           func(event uintptr) Point
	cgEventCreateMouseEvent      func(source uintptr, typ uint32, pos Point, button uint32) uintptr
	cgEventCreateKeyboardEvent   func(source uintptr, key uint16, down bool) uintptr
	cgEventCreateScrollWheelEvt2 func(source uintptr, units uint32, count uint32, w1, w2, w3 int32) uintptr
	cgEventSetIntegerValueField  func(event uintptr, field uint32, value int64)
	cgEventSetFlags              func(event uintptr, flags uint64)
	cgEventSetType               func(event uintptr, typ uint32)
	cgEventPost                  func(tap uint32, event uintptr)
	cfRelease                    func(obj uintptr)

	cgPreflightScreenCaptureAccess func() bool
	cgRequestScreenCaptureAccess   func() bool
	axIsProcessTrusted             func() bool
	axIsProcessTrustedWithOptions  func(options uintptr) bool

	cfStringCreateWithCString func(alloc uintptr, s string, encoding uint32) uintptr
	cfDictionaryCreate        func(alloc uintptr, keys, values *uintptr, n int, keyCB, valueCB uintptr) uintptr
	kCFBooleanTrue            uintptr
	kCFTypeDictKeyCallBacks   uintptr
	kCFTypeDictValueCallBacks uintptr
)

const (
	frameworks       = "/System/Library/Frameworks/"
	coreGraphics     = frameworks + "CoreGraphics.framework/CoreGraphics"
	coreFoundation   = frameworks + "CoreFoundation.framework/CoreFoundation"
	appServices      = frameworks + "ApplicationServices.framework/ApplicationServices"
	cfStringEncUTF8  = 0x08000100
	maxDisplayLookup = 32
)

func load() error {
	loadOnce.Do(func() {
		defer func() {
			if r := recover(); r != nil { // a symbol missing on an old macOS
				loadErr = fmt.Errorf("macOS frameworks: %v", r)
			}
		}()
		cg, err := purego.Dlopen(coreGraphics, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = err
			return
		}
		cf, err := purego.Dlopen(coreFoundation, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = err
			return
		}
		as, err := purego.Dlopen(appServices, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = err
			return
		}
		purego.RegisterLibFunc(&cgGetActiveDisplayList, cg, "CGGetActiveDisplayList")
		purego.RegisterLibFunc(&cgDisplayBounds, cg, "CGDisplayBounds")
		purego.RegisterLibFunc(&cgMainDisplayID, cg, "CGMainDisplayID")
		purego.RegisterLibFunc(&cgDisplayIsBuiltin, cg, "CGDisplayIsBuiltin")
		purego.RegisterLibFunc(&cgDisplayPixelsWide, cg, "CGDisplayPixelsWide")
		purego.RegisterLibFunc(&cgDisplayPixelsHigh, cg, "CGDisplayPixelsHigh")
		purego.RegisterLibFunc(&cgEventSourceCreate, cg, "CGEventSourceCreate")
		purego.RegisterLibFunc(&cgEventCreate, cg, "CGEventCreate")
		purego.RegisterLibFunc(&cgEventGetLocation, cg, "CGEventGetLocation")
		purego.RegisterLibFunc(&cgEventCreateMouseEvent, cg, "CGEventCreateMouseEvent")
		purego.RegisterLibFunc(&cgEventCreateKeyboardEvent, cg, "CGEventCreateKeyboardEvent")
		purego.RegisterLibFunc(&cgEventCreateScrollWheelEvt2, cg, "CGEventCreateScrollWheelEvent2")
		purego.RegisterLibFunc(&cgEventSetIntegerValueField, cg, "CGEventSetIntegerValueField")
		purego.RegisterLibFunc(&cgEventSetFlags, cg, "CGEventSetFlags")
		purego.RegisterLibFunc(&cgEventSetType, cg, "CGEventSetType")
		purego.RegisterLibFunc(&cgEventPost, cg, "CGEventPost")
		purego.RegisterLibFunc(&cgPreflightScreenCaptureAccess, cg, "CGPreflightScreenCaptureAccess")
		purego.RegisterLibFunc(&cgRequestScreenCaptureAccess, cg, "CGRequestScreenCaptureAccess")
		purego.RegisterLibFunc(&cfRelease, cf, "CFRelease")
		purego.RegisterLibFunc(&cfStringCreateWithCString, cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&cfDictionaryCreate, cf, "CFDictionaryCreate")
		purego.RegisterLibFunc(&axIsProcessTrusted, as, "AXIsProcessTrusted")
		purego.RegisterLibFunc(&axIsProcessTrustedWithOptions, as, "AXIsProcessTrustedWithOptions")
		sym := func(lib uintptr, name string) uintptr {
			p, err := purego.Dlsym(lib, name)
			if err != nil {
				panic(name + ": " + err.Error())
			}
			return p
		}
		kCFBooleanTrue = *(*uintptr)(ptr(sym(cf, "kCFBooleanTrue")))
		kCFTypeDictKeyCallBacks = sym(cf, "kCFTypeDictionaryKeyCallBacks")
		kCFTypeDictValueCallBacks = sym(cf, "kCFTypeDictionaryValueCallBacks")
	})
	return loadErr
}

// Display is one active display.
type Display struct {
	ID            uint32
	Bounds        Rect // in points, global coordinates
	PixelsWide    int
	PixelsHigh    int
	Main, Builtin bool
}

// Displays lists the active displays in the order AVFoundation numbers its
// "Capture screen N" devices.
func Displays() ([]Display, error) {
	if err := load(); err != nil {
		return nil, err
	}
	var ids [maxDisplayLookup]uint32
	var n uint32
	if rc := cgGetActiveDisplayList(maxDisplayLookup, &ids[0], &n); rc != 0 {
		return nil, fmt.Errorf("CGGetActiveDisplayList: error %d", rc)
	}
	main := cgMainDisplayID()
	out := make([]Display, 0, n)
	for _, id := range ids[:n] {
		out = append(out, Display{
			ID: id, Bounds: cgDisplayBounds(id),
			PixelsWide: int(cgDisplayPixelsWide(id)), PixelsHigh: int(cgDisplayPixelsHigh(id)),
			Main: id == main, Builtin: cgDisplayIsBuiltin(id) != 0,
		})
	}
	return out, nil
}

// ScreenRecordingAllowed reports whether this app may capture the screen.
func ScreenRecordingAllowed() bool {
	return load() == nil && cgPreflightScreenCaptureAccess()
}

// RequestScreenRecording asks macOS to show the Screen Recording prompt (once;
// afterwards the user has to allow it in System Settings).
func RequestScreenRecording() bool {
	return load() == nil && cgRequestScreenCaptureAccess()
}

// AccessibilityAllowed reports whether this app may control the mouse and
// keyboard.
func AccessibilityAllowed() bool {
	return load() == nil && axIsProcessTrusted()
}

// RequestAccessibility adds the app to the Accessibility list and asks
// macOS to point the user to it.
func RequestAccessibility() bool {
	if load() != nil {
		return false
	}
	key := cfStringCreateWithCString(0, "AXTrustedCheckOptionPrompt", cfStringEncUTF8)
	if key == 0 {
		return axIsProcessTrusted()
	}
	defer cfRelease(key)
	val := kCFBooleanTrue
	dict := cfDictionaryCreate(0, &key, &val, 1, kCFTypeDictKeyCallBacks, kCFTypeDictValueCallBacks)
	if dict == 0 {
		return axIsProcessTrusted()
	}
	defer cfRelease(dict)
	return axIsProcessTrustedWithOptions(dict)
}

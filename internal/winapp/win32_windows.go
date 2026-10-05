//go:build windows

package winapp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
	kernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole   = kernel32.NewProc("AttachConsole")
)

const (
	seeMaskNoCloseProcess = 0x00000040
	swShowNormal          = 1
	errorCancelled        = 1223

	mbOK            = 0x00000000
	mbOKCancel      = 0x00000001
	mbYesNo         = 0x00000004
	mbIconInfo      = 0x00000040
	mbIconWarning   = 0x00000030
	mbIconError     = 0x00000010
	mbIconQuestion  = 0x00000020
	mbSetForeground = 0x00010000
	idOK            = 1
	idYes           = 6
)

// Paths used by the installed app.
var (
	InstallDir   = filepath.Join(os.Getenv("ProgramFiles"), "Windstream")
	InstalledExe = filepath.Join(InstallDir, "Windstream.exe")
	DataDir      = filepath.Join(os.Getenv("ProgramData"), "Windstream")
)

type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           uintptr
	verb           *uint16
	file           *uint16
	params         *uint16
	dir            *uint16
	nShow          int32
	hInstApp       uintptr
	idList         uintptr
	class          *uint16
	hkeyClass      uintptr
	hotKey         uint32
	hIconOrMonitor uintptr
	hProcess       windows.Handle
}

// ErrCancelled means the user declined the UAC prompt.
var ErrCancelled = errors.New("administrator permission was declined")

// runElevated starts exe with args through the UAC prompt and waits for it.
func runElevated(exe string, args string) (uint32, error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(args)
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	sei := shellExecuteInfo{fMask: seeMaskNoCloseProcess, verb: verb, file: file, params: params, dir: dir, nShow: swShowNormal}
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	r, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	if r == 0 {
		if errno, ok := err.(syscall.Errno); ok && errno == errorCancelled {
			return 0, ErrCancelled
		}
		return 0, fmt.Errorf("ShellExecuteEx: %w", err)
	}
	if sei.hProcess == 0 {
		return 0, nil
	}
	defer windows.CloseHandle(sei.hProcess)
	_, _ = windows.WaitForSingleObject(sei.hProcess, windows.INFINITE)
	var code uint32
	_ = windows.GetExitCodeProcess(sei.hProcess, &code)
	return code, nil
}

func isElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

func currentUserSID() (string, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}

func messageBox(text string, flags uint32) int32 {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("Windstream")
	r, _ := windows.MessageBox(0, t, c, flags|mbSetForeground)
	return r
}

// ShowError reports a fatal error to the user (the app has no console).
func ShowError(err error) { messageBox(err.Error(), mbOK|mbIconError) }

// openURL opens a URL in the user's browser. From an elevated process it goes
// through explorer.exe, which hands it to the (unelevated) desktop shell so
// the browser never runs as administrator.
func openURL(url string) {
	if isElevated() {
		cmd := exec.Command(filepath.Join(os.Getenv("WINDIR"), "explorer.exe"), url)
		_ = cmd.Start()
		return
	}
	u, _ := windows.UTF16PtrFromString(url)
	op, _ := windows.UTF16PtrFromString("open")
	_ = windows.ShellExecute(0, op, u, nil, nil, swShowNormal)
}

// hidden runs a system tool without flashing a console window.
func hidden(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

func hiddenCmdLine(exe, cmdline string) *exec.Cmd {
	cmd := exec.Command(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW, CmdLine: cmdline}
	return cmd
}

func system32(name string) string { return filepath.Join(os.Getenv("WINDIR"), "System32", name) }

// AttachParentConsole makes CLI subcommands print to the terminal they were
// started from (the exe is a GUI-subsystem program so it has no console of
// its own).
func AttachParentConsole() {
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = f
	}
}

// createShortcut writes a .lnk through the WScript.Shell COM object.
func createShortcut(lnk, target, args, desc string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		var oe *ole.OleError
		if !errors.As(err, &oe) || oe.Code() != 1 {
			return err
		}
	}
	defer ole.CoUninitialize()
	unk, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return err
	}
	defer unk.Release()
	ws, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer ws.Release()
	v, err := oleutil.CallMethod(ws, "CreateShortcut", lnk)
	if err != nil {
		return err
	}
	sc := v.ToIDispatch()
	defer sc.Release()
	for k, val := range map[string]string{"TargetPath": target, "Arguments": args, "Description": desc,
		"WorkingDirectory": filepath.Dir(target), "IconLocation": target + ",0"} {
		if _, err := oleutil.PutProperty(sc, k, val); err != nil {
			return err
		}
	}
	_, err = oleutil.CallMethod(sc, "Save")
	return err
}

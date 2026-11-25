//go:build windows

package winapp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"

	"github.com/coral-coder/windstream/internal/control"
	"github.com/coral-coder/windstream/internal/safe"
)

// The control panel is a window of its own (WebView2, part of Windows),
// not a page in the browser, and no port is opened for it. The window runs
// in its own process (`--panel`), started by the streaming server: if it
// ever fails, streaming does not. The two talk over the child's stdin and
// stdout, one JSON object per line: API requests one way (see
// control.PanelBridge), answers and commands ("focus") the other.

// showPanelEvent is signalled to open (or bring forward) the window: by
// the launcher on double-click, and by the tray menu.
const showPanelEvent = `Local\WindstreamShowPanel`

const panelFlag = "--panel"

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procShowWindow               = user32.NewProc("ShowWindow")
	procIsIconic                 = user32.NewProc("IsIconic")
	procSendMessageW             = user32.NewProc("SendMessageW")
	procLoadImageW               = user32.NewProc("LoadImageW")
)

// ErrNotRunning means no Windstream is ready to show its window.
var ErrNotRunning = errors.New("Windstream is not running yet")

// ShowPanel asks the running Windstream to show its window.
func ShowPanel() error {
	name, _ := windows.UTF16PtrFromString(showPanelEvent)
	ev, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		return ErrNotRunning
	}
	defer windows.CloseHandle(ev)
	// This process has the user's attention (they just clicked): let the
	// window come to the front rather than flash in the taskbar.
	const asfwAny = ^uintptr(0)
	_, _, _ = procAllowSetForegroundWindow.Call(asfwAny)
	return windows.SetEvent(ev)
}

// HostPanel (control.PanelHost) waits for requests to show the window.
func (p *platform) HostPanel(b *control.PanelBridge) {
	sid, err := currentUserSID()
	if err != nil {
		return
	}
	// The launcher runs unelevated: allow this user at medium integrity to
	// signal the event, nobody else.
	sd, err := windows.SecurityDescriptorFromString("D:(A;;0x1F0003;;;SY)(A;;0x1F0003;;;BA)(A;;0x1F0003;;;" + sid + ")S:(ML;;NW;;;ME)")
	if err != nil {
		return
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, _ := windows.UTF16PtrFromString(showPanelEvent)
	ev, err := windows.CreateEvent(sa, 0, 0, name)
	if err != nil {
		return
	}
	pw := &panelWindow{bridge: b, log: b.Log()}
	safe.Go(pw.log, "control window requests", func() {
		for {
			if r, _ := windows.WaitForSingleObject(ev, windows.INFINITE); r != windows.WAIT_OBJECT_0 {
				return
			}
			pw.show()
		}
	})
}

// panelWindow is the server's side of the window process.
type panelWindow struct {
	bridge *control.PanelBridge
	log    *slog.Logger

	mu  sync.Mutex
	cmd *exec.Cmd
	out *lockedWriter
}

func (pw *panelWindow) show() {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	if pw.cmd != nil {
		_, _ = pw.out.Write([]byte(`{"cmd":"focus"}` + "\n"))
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, panelFlag)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedBuilder{b: &stderr, max: 4096}
	if err := cmd.Start(); err != nil {
		pw.log.Error("could not open the Windstream window", "error", err)
		return
	}
	pw.cmd, pw.out = cmd, &lockedWriter{w: stdin}
	out := pw.out
	safe.Go(pw.log, "control window", func() {
		err := pw.bridge.Serve(stdout, out)
		werr := cmd.Wait()
		_ = stdin.Close()
		if werr != nil || err != nil {
			pw.log.Warn("the Windstream window closed unexpectedly", "error", errors.Join(err, werr), "detail", strings.TrimSpace(stderr.String()))
		}
		pw.mu.Lock()
		pw.cmd, pw.out = nil, nil
		pw.mu.Unlock()
	})
}

// PanelWindow implements --panel: the Windstream window. It closes when
// the streaming server that started it stops.
func PanelWindow() error {
	if _, err := webviewloader.GetInstalledVersion(); err != nil {
		const idYes = 6
		if messageBox("The Windstream window needs Microsoft Edge WebView2, which is part of Windows but missing or damaged on this PC.\n\nOpen Microsoft's download page?", mbYesNo|mbIconWarning) == idYes {
			openURL("https://go.microsoft.com/fwlink/p/?LinkId=2124703")
		}
		return nil
	}
	page, err := control.PanelHTML()
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  filepath.Join(DataDir, "webview"),
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title: "Windstream", Width: 1180, Height: 920, Center: true,
		},
	})
	if w == nil {
		return errors.New("the Windstream window could not be created")
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	setWindowIcon(hwnd)

	client := &bridgeClient{w: json.NewEncoder(os.Stdout), pending: map[int]chan control.BridgeResponse{}}
	if err := w.Bind("windstreamCall", client.call); err != nil {
		return err
	}
	if err := w.Bind("windstreamOpen", func(url string) {
		if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
			openURL(url)
		}
	}); err != nil {
		return err
	}
	go func() {
		client.readLoop(os.Stdin, func(cmd string) {
			if cmd == "focus" {
				w.Dispatch(func() { bringToFront(hwnd) })
			}
		})
		w.Terminate() // the server stopped (quit, update, uninstall)
	}()
	w.SetHtml(page)
	bringToFront(hwnd)
	w.Run()
	return nil
}

func bringToFront(hwnd uintptr) {
	const swRestore, swShow = 9, 5
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		_, _, _ = procShowWindow.Call(hwnd, swRestore)
	} else {
		_, _, _ = procShowWindow.Call(hwnd, swShow)
	}
	_, _, _ = procSetForegroundWindow.Call(hwnd)
}

// setWindowIcon uses the exe's icon (resource "APPICON") for the window and
// its taskbar button.
func setWindowIcon(hwnd uintptr) {
	var hinst windows.Handle
	if windows.GetModuleHandleEx(0, nil, &hinst) != nil {
		return
	}
	name, _ := syscall.UTF16PtrFromString("APPICON")
	const imageIcon, lrDefaultSize, wmSetIcon = 1, 0x40, 0x80
	for _, size := range []struct{ which, px uintptr }{{1, 32}, {0, 16}} { // ICON_BIG, ICON_SMALL
		icon, _, _ := procLoadImageW.Call(uintptr(hinst), uintptr(unsafe.Pointer(name)), imageIcon, size.px, size.px, 0)
		if icon == 0 {
			icon, _, _ = procLoadImageW.Call(uintptr(hinst), uintptr(unsafe.Pointer(name)), imageIcon, 0, 0, lrDefaultSize)
		}
		if icon != 0 {
			_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, size.which, icon)
		}
	}
}

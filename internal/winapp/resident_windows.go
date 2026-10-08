//go:build windows

package winapp

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/coral-coder/windstream/internal/control"
)

//go:embed icon.ico
var trayIcon []byte

// RunFunc starts the controller and blocks until ctx is cancelled.
type RunFunc func(ctx context.Context, platform control.Platform, onReady func(*control.Controller)) error

type platform struct {
	quit        func()
	onUninstall func(removeData bool) // the dashboard asked to uninstall
	mu          sync.Mutex
	panelURL    string
}

// PanelReady records the dashboard address for the tray and for the
// launcher (which reads it from the registry; it cannot read ProgramData).
func (p *platform) PanelReady(url string) {
	p.mu.Lock()
	p.panelURL = url
	p.mu.Unlock()
	if k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, appRegKey, registry.SET_VALUE); err == nil {
		_ = k.SetStringValue("PanelURL", url)
		k.Close()
	}
}

func (p *platform) SteamExe() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("SteamExe")
	if err != nil || v == "" {
		return ""
	}
	v = strings.ReplaceAll(v, "/", `\`)
	if _, err := os.Stat(v); err != nil {
		return ""
	}
	return v
}

// Uninstall shuts the worker down cleanly (restoring the display and closing
// router ports); the supervisor then removes the app.
func (p *platform) Uninstall(removeData bool) error {
	p.onUninstall(removeData)
	return nil
}

func (p *platform) Quit() { p.quit() }

// OpenFolder shows a folder in File Explorer.
func (p *platform) OpenFolder(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	openURL(path)
	return nil
}

// SystemInfo describes the Windows version for diagnostics.
func (p *platform) SystemInfo() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "Windows (version unknown)"
	}
	defer k.Close()
	name, _, _ := k.GetStringValue("ProductName")
	disp, _, _ := k.GetStringValue("DisplayVersion")
	build, _, _ := k.GetStringValue("CurrentBuild")
	ubr, _, _ := k.GetIntegerValue("UBR")
	if b, err := strconv.Atoi(build); err == nil && b >= 22000 {
		name = strings.Replace(name, "Windows 10", "Windows 11", 1) // ProductName still says 10
	}
	return fmt.Sprintf("%s %s (build %s.%d)", name, disp, build, ubr)
}

// Resident is the long-running app started at logon: one instance per
// session, a tray icon, and a supervisor that runs the streaming server in a
// separate worker process (see supervise). If the worker ever dies — a Go
// panic in any goroutine, a fatal runtime error, a crash inside a driver or
// system DLL — the supervisor records why and starts a fresh one within
// seconds; viewers' browsers reconnect on their own.
func Resident() error {
	mname, _ := windows.UTF16PtrFromString(residentMutex)
	mutex, err := windows.CreateMutex(nil, true, mname)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil // already running in this session
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(mutex)
	defer windows.ReleaseMutex(mutex)

	ename, _ := windows.UTF16PtrFromString(quitEvent)
	ev, err := windows.CreateEvent(nil, 1, 0, ename)
	if ev == 0 {
		return err
	}
	// The event can outlive a previous run (e.g. held by a worker that has
	// not exited yet) and may still be set from that run's quit.
	_ = windows.ResetEvent(ev)
	defer windows.CloseHandle(ev)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	// quit also signals the event, which the worker waits on to shut down
	// cleanly (restoring the display and closing router ports).
	quit := func() { once.Do(func() { _ = windows.SetEvent(ev); cancel() }) }
	go func() {
		_, _ = windows.WaitForSingleObject(ev, windows.INFINITE)
		quit()
	}()

	// The server starts first and does not depend on the tray: at logon the
	// taskbar may not be ready, and a tray failure must never mean no
	// server.
	sup := newSupervisor()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sup.run(ctx, quit)
		systray.Quit()
	}()

	onReady := func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("Windstream")
		systray.SetTooltip("Windstream – starting")
		mOpen := systray.AddMenuItem("Open Windstream", "Open the dashboard")
		mLink := systray.AddMenuItem("Open my stream link", "Open the address you play from")
		mLogs := systray.AddMenuItem("Open logs folder", "Show Windstream's log files")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit Windstream", "Stop streaming and close")
		systray.SetOnTapped(openPanel)
		go func() {
			t := time.NewTicker(3 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-mOpen.ClickedCh:
					openPanel()
				case <-mLink.ClickedCh:
					if st := readWorkerStatus(); st.Link != "" {
						openURL(st.Link)
					} else {
						openPanel()
					}
				case <-mLogs.ClickedCh:
					_ = os.MkdirAll(logsDir(), 0o700)
					openURL(logsDir())
				case <-mQuit.ClickedCh:
					quit()
				case <-t.C:
					systray.SetTooltip(sup.tooltip())
				}
			}
		}()
	}
	systray.Run(onReady, func() {})
	// The tray is gone: either we are quitting, or it could not be created
	// (e.g. no taskbar yet). Either way keep supervising until told to quit.
	<-done
	if u := sup.uninstallRequested(); u != nil {
		performUninstall(*u, false)
	}
	return nil
}

// openPanel opens the dashboard, but only if it is Windstream answering at
// the saved address; otherwise it explains instead of sending the user to
// whatever else might be listening there.
func openPanel() {
	url := PanelURL()
	if runningVersion(url) != "" {
		openURL(url)
		return
	}
	go messageBox("Windstream is still starting (or restarting after a problem). Try again in a few seconds.\n\nIf this keeps happening, use \"Open logs folder\" in the tray menu.", mbOK|mbIconInfo)
}

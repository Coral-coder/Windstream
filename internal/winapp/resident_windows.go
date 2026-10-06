//go:build windows

package winapp

import (
	"context"
	_ "embed"
	"errors"
	"os"
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
	quit      func()
	uninstall *bool // set when the dashboard asked to uninstall
	mu        sync.Mutex
	panelURL  string
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

func (p *platform) panel() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.panelURL != "" {
		return p.panelURL
	}
	return PanelURL()
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

// Uninstall shuts the app down cleanly (restoring the display and closing
// router ports) and then removes it, all within this process.
func (p *platform) Uninstall(removeData bool) error {
	p.mu.Lock()
	p.uninstall = &removeData
	p.mu.Unlock()
	p.quit()
	return nil
}

func (p *platform) Quit() { p.quit() }

// Resident is the long-running app started at logon: one instance per
// session, a tray icon, and the controller underneath.
func Resident(run RunFunc) error {
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
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ev)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	done := make(chan error, 1)
	quit := func() { once.Do(cancel) }
	go func() {
		_, _ = windows.WaitForSingleObject(ev, windows.INFINITE)
		quit()
	}()

	var ctrl *control.Controller
	var ctrlMu sync.Mutex
	plat := &platform{quit: quit}

	onReady := func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("Windstream")
		systray.SetTooltip("Windstream – starting")
		mOpen := systray.AddMenuItem("Open Windstream", "Open the dashboard")
		mLink := systray.AddMenuItem("Open my stream link", "Open the address you play from")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit Windstream", "Stop streaming and close")
		systray.SetOnTapped(func() { openURL(plat.panel()) })

		go func() {
			done <- run(ctx, plat, func(c *control.Controller) {
				ctrlMu.Lock()
				ctrl = c
				ctrlMu.Unlock()
			})
			systray.Quit()
		}()
		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-mOpen.ClickedCh:
					openURL(plat.panel())
				case <-mLink.ClickedCh:
					ctrlMu.Lock()
					c := ctrl
					ctrlMu.Unlock()
					if c != nil && c.Link() != "" {
						openURL(c.Link())
					} else {
						openURL(plat.panel())
					}
				case <-mQuit.ClickedCh:
					quit()
				case <-t.C:
					ctrlMu.Lock()
					c := ctrl
					ctrlMu.Unlock()
					if c != nil {
						systray.SetTooltip(c.TrayStatus())
					}
				}
			}
		}()
	}
	systray.Run(onReady, func() { quit() })
	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(15 * time.Second):
	}
	plat.mu.Lock()
	uninstall := plat.uninstall
	plat.mu.Unlock()
	if uninstall != nil {
		performUninstall(*uninstall)
		return nil
	}
	return runErr
}

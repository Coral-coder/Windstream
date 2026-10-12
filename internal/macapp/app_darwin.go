//go:build darwin

package macapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coral-coder/windstream/internal/appver"
	"github.com/coral-coder/windstream/internal/control"
	"github.com/coral-coder/windstream/internal/deps"
	"github.com/coral-coder/windstream/internal/macos"
)

// Version is the running build's version (set by main).
var Version = "dev"

// RunFunc starts the controller and blocks until ctx is cancelled.
type RunFunc func(ctx context.Context, platform control.Platform, onReady func(*control.Controller)) error

const defaultPanelURL = "http://127.0.0.1:47333/"

// restartExitCode makes launchd start the server again (KeepAlive restarts
// on any non-zero exit).
const restartExitCode = 75

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/tmp"
	}
	return h
}

// DataDir holds settings, logs and the downloaded encoder.
func DataDir() string {
	return filepath.Join(homeDir(), "Library", "Application Support", "Windstream")
}

func agentPlistPath() string {
	return filepath.Join(homeDir(), "Library", "LaunchAgents", Label+".plist")
}

func panelURLFile() string { return filepath.Join(DataDir(), "panel-url") }

// panelURL is the dashboard address the server last published.
func panelURL() string {
	if b, err := os.ReadFile(panelURLFile()); err == nil {
		if u := strings.TrimSpace(string(b)); strings.HasPrefix(u, "http://127.0.0.1:") {
			return u
		}
	}
	return defaultPanelURL
}

// runningVersion returns the version of the Windstream answering on the
// dashboard, or "" if none is.
func runningVersion(u string) string {
	c := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Get(u + "api/state")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var st struct {
		Version     *string `json:"version"`
		SetupNeeded *bool   `json:"setup_needed"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&st) != nil || st.Version == nil || st.SetupNeeded == nil {
		return ""
	}
	return *st.Version
}

func openURL(u string) { _ = exec.Command("/usr/bin/open", u).Start() }

// message shows a dialog (best effort; the app has no window of its own).
func message(text string) {
	script := fmt.Sprintf(`display dialog %s with title "Windstream" buttons {"OK"} default button "OK" with icon note`, strconv.Quote(text))
	_ = exec.Command("/usr/bin/osascript", "-e", script).Run()
}

func uid() string { return strconv.Itoa(os.Getuid()) }

// installTarget is where the app lives: /Applications when the user may
// write there (any admin account), otherwise ~/Applications.
func installTarget() string {
	if f, err := os.CreateTemp("/Applications", ".windstream-probe-*"); err == nil {
		name := f.Name()
		f.Close()
		os.Remove(name)
		return filepath.Join("/Applications", AppName)
	}
	dir := filepath.Join(homeDir(), "Applications")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, AppName)
}

func stopAgent() {
	_ = exec.Command("/bin/launchctl", "bootout", "gui/"+uid()+"/"+Label).Run()
	// Wait for the old server to let go of its ports.
	for i := 0; i < 40 && runningVersion(panelURL()) != ""; i++ {
		time.Sleep(250 * time.Millisecond)
	}
}

func startAgent(exe string) error {
	if err := os.MkdirAll(filepath.Dir(agentPlistPath()), 0o755); err != nil {
		return err
	}
	logs := filepath.Join(DataDir(), "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(agentPlistPath(), []byte(AgentPlist(exe, logs)), 0o644); err != nil {
		return err
	}
	out, err := exec.Command("/bin/launchctl", "bootstrap", "gui/"+uid(), agentPlistPath()).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "already") {
		// Older macOS: the legacy verb.
		if out2, err2 := exec.Command("/bin/launchctl", "load", "-w", agentPlistPath()).CombinedOutput(); err2 != nil {
			return fmt.Errorf("launchctl: %s %s", strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
		}
	}
	return nil
}

// copyBundle installs the app at target, replacing an older copy.
func copyBundle(src, target string) error {
	tmp := target + ".installing"
	_ = os.RemoveAll(tmp)
	if out, err := exec.Command("/usr/bin/ditto", src, tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("copy app: %v %s", err, out)
	}
	// Downloaded apps are quarantined; the installed copy is trusted by
	// the user's choice to open it.
	_ = exec.Command("/usr/bin/xattr", "-dr", "com.apple.quarantine", tmp).Run()
	_ = os.RemoveAll(target)
	return os.Rename(tmp, target)
}

// Launch is what happens when Windstream.app is opened: install or update
// it if needed, make sure the server is running, and open the dashboard.
func Launch(run RunFunc) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	bundle, isBundle := BundlePath(exe)
	if !isBundle {
		return Agent(run) // a bare binary (development): run in the foreground
	}
	target := installTarget()
	running := runningVersion(panelURL())
	installed := bundle == target

	if running != "" && !installed && appver.Newer(running, Version) {
		message("A newer Windstream (" + running + ") is already installed. Opening it instead of this older copy.")
		openURL(panelURL())
		return nil
	}
	if installed && running != "" {
		openURL(panelURL()) // already running: just show the dashboard
		return nil
	}
	if running != "" {
		stopAgent() // updating: stop the old version first
	}
	if !installed {
		if err := copyBundle(bundle, target); err != nil {
			message("Windstream could not be installed in " + filepath.Dir(target) + ":\n\n" + err.Error())
			return err
		}
	}
	if err := startAgent(filepath.Join(target, "Contents", "MacOS", "Windstream")); err != nil {
		message("Windstream could not start:\n\n" + err.Error())
		return err
	}
	for i := 0; i < 120; i++ {
		if runningVersion(panelURL()) != "" {
			openURL(panelURL())
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	message("Windstream is starting. Open it again in a moment to see the dashboard.")
	return nil
}

// CheckPermissions prints the current permission state for a running
// server, which cannot see changes itself until it restarts (--tcc-check).
func CheckPermissions() {
	fmt.Printf("screen=%t access=%t\n", macos.ScreenRecordingAllowed(), macos.AccessibilityAllowed())
}

// platform is the control.Platform for macOS.
type platform struct {
	quit        func()
	onUninstall func(removeData bool)

	mu     sync.Mutex
	screen bool
	access bool
}

func (p *platform) PanelReady(url string) {
	_ = os.MkdirAll(DataDir(), 0o700)
	_ = os.WriteFile(panelURLFile(), []byte(url), 0o600)
}

func (p *platform) SteamExe() string {
	if _, err := os.Stat("/Applications/Steam.app"); err == nil {
		return "/Applications/Steam.app"
	}
	return ""
}

func (p *platform) Uninstall(removeData bool) error {
	p.onUninstall(removeData)
	return nil
}

func (p *platform) Quit() { p.quit() }

// OpenFolder shows a folder in Finder.
func (p *platform) OpenFolder(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return exec.Command("/usr/bin/open", path).Start()
}

// SystemInfo describes the Mac for diagnostics.
func (p *platform) SystemInfo() string {
	ver, _ := exec.Command("/usr/bin/sw_vers", "-productVersion").Output()
	model, _ := exec.Command("/usr/sbin/sysctl", "-n", "hw.model").Output()
	chip, _ := exec.Command("/usr/sbin/sysctl", "-n", "machdep.cpu.brand_string").Output()
	return fmt.Sprintf("macOS %s, %s, %s", strings.TrimSpace(string(ver)), strings.TrimSpace(string(model)), strings.TrimSpace(string(chip)))
}

func (p *platform) Permissions() []deps.Component {
	p.mu.Lock()
	screen, access := p.screen, p.access
	p.mu.Unlock()
	comp := func(id, name, purpose string, ok bool, how string) deps.Component {
		c := deps.Component{ID: id, Name: name, Purpose: purpose, State: deps.StateReady}
		if !ok {
			c.State, c.Detail, c.Action = deps.StateError, how, "allow"
		}
		return c
	}
	return []deps.Component{
		comp("perm-screen", "Screen Recording permission", "Lets Windstream see the screen", screen,
			"Needed to stream the screen: System Settings → Privacy & Security → Screen & System Audio Recording → turn on Windstream"),
		comp("perm-access", "Accessibility permission", "Lets Windstream move the mouse and type", access,
			"Needed for mouse and keyboard: System Settings → Privacy & Security → Accessibility → turn on Windstream"),
	}
}

func (p *platform) RequestPermission(id string) error {
	switch id {
	case "perm-screen":
		macos.RequestScreenRecording()
		openURL("x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture")
	case "perm-access":
		macos.RequestAccessibility()
		openURL("x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility")
	default:
		return errors.New("unknown permission")
	}
	return nil
}

// checkPermissions asks a fresh process (macOS caches the answer per
// process until it restarts).
func checkPermissions() (screen, access bool, ok bool) {
	exe, err := os.Executable()
	if err != nil {
		return false, false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "--tcc-check").Output()
	if err != nil {
		return false, false, false
	}
	s := string(out)
	return strings.Contains(s, "screen=true"), strings.Contains(s, "access=true"), true
}

// Agent runs the streaming server (launched by launchd at login).
func Agent(run RunFunc) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	exitCode := 0
	var removeData *bool
	var mu sync.Mutex
	plat := &platform{quit: cancel, screen: macos.ScreenRecordingAllowed(), access: macos.AccessibilityAllowed()}
	plat.onUninstall = func(rd bool) {
		mu.Lock()
		removeData = &rd
		mu.Unlock()
		cancel()
	}
	// Ask for both permissions up front, while the user is at the Mac
	// setting Windstream up, rather than when someone first connects.
	if !plat.screen {
		macos.RequestScreenRecording()
	}
	if !plat.access {
		macos.RequestAccessibility()
	}
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			plat.mu.Lock()
			missing := !plat.screen || !plat.access
			plat.mu.Unlock()
			if !missing {
				continue
			}
			screen, access, ok := checkPermissions()
			if !ok {
				continue
			}
			plat.mu.Lock()
			gotScreen := screen && !plat.screen
			plat.screen, plat.access = plat.screen || screen, plat.access || access
			plat.mu.Unlock()
			if gotScreen {
				// Screen recording only takes effect in a new process.
				mu.Lock()
				exitCode = restartExitCode
				mu.Unlock()
				cancel()
				return
			}
		}
	}()

	err := run(ctx, plat, nil)
	mu.Lock()
	rd, code := removeData, exitCode
	mu.Unlock()
	if rd != nil {
		uninstall(*rd)
		return nil // not reached when launchd stops us first
	}
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "startup failed:", err)
		os.Exit(1)
	}
	os.Exit(code)
	return nil
}

// uninstall removes the app, its login item and (optionally) its data.
func uninstall(removeData bool) {
	_ = os.Remove(agentPlistPath())
	if exe, err := os.Executable(); err == nil {
		if bundle, ok := BundlePath(exe); ok {
			_ = os.RemoveAll(bundle)
		}
	}
	if removeData {
		_ = os.RemoveAll(DataDir())
	}
	// Last: this stops this very process.
	_ = exec.Command("/bin/launchctl", "bootout", "gui/"+uid()+"/"+Label).Run()
	os.Exit(0)
}

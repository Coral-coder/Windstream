//go:build windows

package winapp

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	taskName      = "Windstream"
	uninstallKey  = `Software\Microsoft\Windows\CurrentVersion\Uninstall\Windstream`
	appRegKey     = `Software\Windstream`
	firewallRule  = "Windstream"
	residentMutex = `Local\WindstreamResident`
	quitEvent     = `Local\WindstreamQuit`
)

func startMenuLink() string {
	return filepath.Join(os.Getenv("ProgramData"), `Microsoft\Windows\Start Menu\Programs\Windstream.lnk`)
}

// Install copies the running exe into Program Files and registers
// everything needed to run at logon. userSID is the account that launched
// the installer (before elevation).
func Install(version, userSID string) error {
	if !isElevated() {
		return errors.New("installation needs administrator rights")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	stopResident(25 * time.Second) // the worker gets 12 s to restore the display, plus margin
	// An uninstall that has not been followed by a reboot yet left our
	// files queued for deletion at boot; cancel that, or Windows would
	// delete the fresh install on the next restart.
	if n := cancelPendingDeletes(InstallDir, DataDir); n > 0 {
		fmt.Fprintf(os.Stderr, "cancelled %d pending deletions from a previous uninstall\n", n)
	}

	if err := os.MkdirAll(InstallDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", InstallDir, err)
	}
	if !samePath(self, InstalledExe) {
		var cerr error
		for i := 0; i < 10; i++ { // the old exe may take a moment to unlock
			if cerr = copyFile(self, InstalledExe); cerr == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if cerr != nil {
			return fmt.Errorf("copy to %s: %w", InstalledExe, cerr)
		}
	}

	if err := secureDataDir(userSID); err != nil {
		return fmt.Errorf("prepare %s: %w", DataDir, err)
	}
	if err := addFirewallRule(); err != nil {
		return fmt.Errorf("firewall: %w", err)
	}
	if err := registerTask(userSID); err != nil {
		return fmt.Errorf("startup task: %w", err)
	}
	_ = createShortcut(startMenuLink(), InstalledExe, "", "Windstream game streaming")
	if err := writeUninstallEntry(version); err != nil {
		return fmt.Errorf("uninstall entry: %w", err)
	}
	if err := runTask(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	return nil
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// secureDataDir creates the data directory readable only by SYSTEM and
// Administrators: it holds password hashes, TOTP secrets and TLS keys. If
// the installer was elevated with a different administrator's credentials
// (standard user), that user is granted access too, because their logon
// task will run without admin rights.
func secureDataDir(userSID string) error {
	if err := os.MkdirAll(DataDir, 0o700); err != nil {
		return err
	}
	sddl := "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if self, err := currentUserSID(); err == nil && userSID != "" && userSID != self {
		sddl += "(A;OICI;FA;;;" + userSID + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(DataDir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return err
	}
	// Logs contain no secrets (passwords are hashed; no TOTP secrets or keys),
	// so let any user on this PC read them for troubleshooting. Inherit from
	// the parent but add read access for the Users group.
	logs := filepath.Join(DataDir, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return err
	}
	logSDDL := "D:AI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)" // BU = Builtin Users: read & execute
	if self, err := currentUserSID(); err == nil && userSID != "" && userSID != self {
		// A standard user's logon task runs without admin rights and must
		// be able to write its own logs.
		logSDDL += "(A;OICI;FA;;;" + userSID + ")"
	}
	if lsd, err := windows.SecurityDescriptorFromString(logSDDL); err == nil {
		if ldacl, _, err := lsd.DACL(); err == nil {
			_ = windows.SetNamedSecurityInfo(logs, windows.SE_FILE_OBJECT,
				windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, ldacl, nil)
		}
	}
	return nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// taskXML: start at the user's logon, in their desktop session, elevated
// without a prompt, never time out, restart after a crash.
func taskXML(userSID string) string {
	return `<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Author>Windstream</Author><Description>Starts Windstream game streaming (tray app) when you sign in. Remove it via Apps &amp; features &gt; Windstream.</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + userSID + `</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + userSID + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>4</Priority>
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(InstalledExe) + `</Command>
      <Arguments>--run</Arguments>
      <WorkingDirectory>` + xmlEscape(InstallDir) + `</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`
}

func registerTask(userSID string) error {
	if userSID == "" {
		var err error
		if userSID, err = currentUserSID(); err != nil {
			return err
		}
	}
	return registerTaskXML(taskXML(userSID))
}

func writeUninstallEntry(version string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	for name, val := range map[string]string{
		"DisplayName":          "Windstream",
		"DisplayVersion":       version,
		"Publisher":            "Windstream",
		"DisplayIcon":          InstalledExe + ",0",
		"InstallLocation":      InstallDir,
		"UninstallString":      `"` + InstalledExe + `" --uninstall`,
		"QuietUninstallString": `"` + InstalledExe + `" --uninstall --keep-data --quiet`,
		"URLInfoAbout":         "https://github.com/coral-coder/windstream",
	} {
		if err := k.SetStringValue(name, val); err != nil {
			return err
		}
	}
	_ = k.SetDWordValue("NoModify", 1)
	_ = k.SetDWordValue("NoRepair", 1)
	if st, err := os.Stat(InstalledExe); err == nil {
		_ = k.SetDWordValue("EstimatedSize", uint32(st.Size()/1024))
	}
	return nil
}

// stopResident asks a running Windstream to quit gracefully (it restores
// the display layout and closes router ports), then waits for it to exit.
func stopResident(timeout time.Duration) {
	name, _ := windows.UTF16PtrFromString(quitEvent)
	if ev, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name); err == nil {
		_ = windows.SetEvent(ev)
		windows.CloseHandle(ev)
	}
	mname, _ := windows.UTF16PtrFromString(residentMutex)
	m, err := windows.OpenMutex(windows.SYNCHRONIZE, false, mname)
	if err != nil {
		return // not running
	}
	defer windows.CloseHandle(m)
	ev, _ := windows.WaitForSingleObject(m, uint32(timeout/time.Millisecond))
	if ev == uint32(windows.WAIT_TIMEOUT) {
		// Hung: ask Task Scheduler to end it.
		stopTask()
		time.Sleep(2 * time.Second)
	} else {
		_ = windows.ReleaseMutex(m)
	}
}

var procRegSetValueExW = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegSetValueExW")

// cancelPendingDeletes removes entries under any of dirs from the
// boot-time PendingFileRenameOperations list (MoveFileEx with
// MOVEFILE_DELAY_UNTIL_REBOOT). It returns how many it removed.
func cancelPendingDeletes(dirs ...string) int {
	const keyPath = `SYSTEM\CurrentControlSet\Control\Session Manager`
	const value = "PendingFileRenameOperations"
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, keyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return 0
	}
	defer k.Close()
	n, typ, err := k.GetValue(value, nil)
	if err != nil || typ != registry.MULTI_SZ || n == 0 {
		return 0
	}
	buf := make([]byte, n)
	if _, _, err := k.GetValue(value, buf); err != nil {
		return 0
	}
	u16 := make([]uint16, len(buf)/2)
	for i := range u16 {
		u16[i] = uint16(buf[2*i]) | uint16(buf[2*i+1])<<8
	}
	// The list is pairs of (source, destination) strings; an empty
	// destination means delete. It ends with an empty source.
	var tokens []string
	start := 0
	for i, c := range u16 {
		if c == 0 {
			tokens = append(tokens, windows.UTF16ToString(u16[start:i]))
			start = i + 1
		}
	}
	var kept []string
	removed := 0
	for i := 0; i+1 < len(tokens); i += 2 {
		src, dst := tokens[i], tokens[i+1]
		if src == "" {
			break
		}
		path := strings.TrimPrefix(strings.TrimPrefix(src, `\??\`), "*")
		ours := false
		for _, d := range dirs {
			if strings.HasPrefix(strings.ToLower(path), strings.ToLower(d)) {
				ours = true
			}
		}
		if ours && dst == "" {
			removed++
			continue
		}
		kept = append(kept, src, dst)
	}
	if removed == 0 {
		return 0
	}
	if len(kept) == 0 {
		_ = k.DeleteValue(value)
		return removed
	}
	var out []uint16
	for _, s := range kept {
		w, _ := windows.UTF16FromString(s) // includes the terminating NUL
		out = append(out, w...)
	}
	out = append(out, 0)
	raw := make([]byte, 2*len(out))
	for i, c := range out {
		raw[2*i], raw[2*i+1] = byte(c), byte(c>>8)
	}
	name, _ := windows.UTF16PtrFromString(value)
	if r, _, _ := procRegSetValueExW.Call(uintptr(k), uintptr(unsafe.Pointer(name)), 0, uintptr(windows.REG_MULTI_SZ),
		uintptr(unsafe.Pointer(&raw[0])), uintptr(len(raw))); r != 0 {
		return 0
	}
	return removed
}

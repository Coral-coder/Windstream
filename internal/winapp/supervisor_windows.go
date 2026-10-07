//go:build windows

package winapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/coral-coder/windstream/internal/control"
)

// workerFlag starts the exe as the streaming server under a supervisor.
const workerFlag = "--worker"

func logsDir() string          { return filepath.Join(DataDir, "logs") }
func workerStatusPath() string { return filepath.Join(DataDir, "status.json") }

// workerStatus is what the worker publishes for the tray.
type workerStatus struct {
	Link    string    `json:"link"`
	Tray    string    `json:"tray"`
	Updated time.Time `json:"updated"`
}

func readWorkerStatus() workerStatus {
	var st workerStatus
	if b, err := os.ReadFile(workerStatusPath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func newSupervisor() *supervisor {
	return &supervisor{logDir: logsDir(), start: startWorker,
		grace: 12 * time.Second, minBackoff: time.Second, maxBackoff: 30 * time.Second}
}

func (s *supervisor) tooltip() string {
	running, crashed, when := s.state()
	if !running {
		if crashed != "" {
			return "Windstream – restarting after a problem"
		}
		return "Windstream – starting"
	}
	st := readWorkerStatus()
	tip := st.Tray
	if tip == "" || time.Since(st.Updated) > 30*time.Second {
		tip = "Windstream – starting"
	}
	if crashed != "" && time.Since(when) < 10*time.Minute {
		tip += " (recovered from a crash)"
	}
	return tip
}

// jobProc is a worker running in its own kill-on-close job object, so a
// crashed worker never leaves encoders (ffmpeg) running behind it.
type jobProc struct {
	cmd *exec.Cmd
	job windows.Handle
}

func startWorker(env []string, out io.Writer) (workerProc, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	job, err := newKillOnCloseJob()
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	cmd := exec.Command(exe, workerFlag)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second // never hang on an inherited pipe
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	if h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid)); err == nil {
		_ = windows.AssignProcessToJobObject(job, h)
		windows.CloseHandle(h)
	}
	return &jobProc{cmd: cmd, job: job}, nil
}

func (p *jobProc) Wait() int {
	_ = p.cmd.Wait()
	code := -1
	if p.cmd.ProcessState != nil {
		code = p.cmd.ProcessState.ExitCode()
	}
	windows.CloseHandle(p.job) // kills anything the worker left running
	return code
}

func (p *jobProc) Kill() { _ = windows.TerminateJobObject(p.job, 1) }

// newKillOnCloseJob creates a job object whose processes are all terminated
// when its last handle closes.
func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// Worker is the streaming server process started by the supervisor. It
// shuts down cleanly when the quit event is set, and reports quit/uninstall
// requests from the dashboard through its exit code.
func Worker(run RunFunc) {
	// Print every goroutine if the process ever dies: the supervisor saves
	// it to logs\crash.log.
	debug.SetTraceback("all")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exitCode := workerExitQuit
	var mu sync.Mutex
	plat := &platform{quit: cancel}
	plat.onUninstall = func(removeData bool) {
		mu.Lock()
		exitCode = workerExitUninstall
		if removeData {
			exitCode = workerExitUninstallData
		}
		mu.Unlock()
		cancel()
	}

	ename, _ := windows.UTF16PtrFromString(quitEvent)
	if ev, err := windows.OpenEvent(windows.SYNCHRONIZE, false, ename); err == nil {
		go func() {
			_, _ = windows.WaitForSingleObject(ev, windows.INFINITE)
			cancel()
		}()
	}

	var ctrl *control.Controller
	var ctrlMu sync.Mutex
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			ctrlMu.Lock()
			c := ctrl
			ctrlMu.Unlock()
			if c == nil {
				continue
			}
			// Publishing the status takes the controller's and the
			// engine's locks, so it doubles as a deadlock watchdog: if it
			// cannot finish in 90 s the server is wedged. Crash on purpose
			// so the supervisor restarts it, with every goroutine's stack
			// in crash.log to show what was stuck.
			done := make(chan struct{})
			go func() {
				writeWorkerStatus(c)
				close(done)
			}()
			select {
			case <-done:
			case <-ctx.Done():
				return
			case <-time.After(90 * time.Second):
				panic("watchdog: the server stopped responding for 90 seconds; restarting it")
			}
		}
	}()

	err := run(ctx, plat, func(c *control.Controller) {
		ctrlMu.Lock()
		ctrl = c
		ctrlMu.Unlock()
	})
	_ = os.Remove(workerStatusPath())
	mu.Lock()
	code := exitCode
	mu.Unlock()
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "startup failed:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func writeWorkerStatus(c *control.Controller) {
	defer func() { _ = recover() }() // status is best-effort
	b, err := json.Marshal(workerStatus{Link: c.Link(), Tray: c.TrayStatus(), Updated: time.Now()})
	if err != nil {
		return
	}
	tmp := workerStatusPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, workerStatusPath())
	}
}

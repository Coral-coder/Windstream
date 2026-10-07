package winapp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// This file holds the platform-independent half of the supervisor (restart
// policy, exit-code protocol, crash records) so it can be tested anywhere;
// supervisor_windows.go supplies process creation and the job object.

// Exit codes a worker uses to tell the supervisor what to do next. Anything
// else (a Go panic exits with 2, a native crash with an NTSTATUS) is a crash
// and the worker is restarted.
const (
	workerExitQuit          = 0
	workerExitUninstall     = 10
	workerExitUninstallData = 11
)

// LastCrashEnv carries a one-line summary of the previous worker's crash to
// its replacement, which shows it on the dashboard.
const LastCrashEnv = "WINDSTREAM_LAST_CRASH"

// workerProc is a started worker.
type workerProc interface {
	// Wait blocks until the worker exits and returns its exit code.
	Wait() int
	// Kill terminates the worker and everything it started.
	Kill()
}

// supervisor runs the worker process and restarts it when it dies.
type supervisor struct {
	logDir string
	start  func(env []string, out io.Writer) (workerProc, error)
	// grace is how long a worker gets to shut down cleanly on quit.
	grace                  time.Duration
	minBackoff, maxBackoff time.Duration

	mu         sync.Mutex
	running    bool
	restarts   int
	lastCrash  string
	lastCrashT time.Time
	uninstall  *bool
}

func (s *supervisor) uninstallRequested() *bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.uninstall
}

func (s *supervisor) state() (running bool, lastCrash string, when time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.lastCrash, s.lastCrashT
}

// run keeps a worker alive until ctx is cancelled or the worker asks to quit
// or uninstall.
func (s *supervisor) run(ctx context.Context, quit func()) {
	backoff := s.minBackoff
	for ctx.Err() == nil {
		began := time.Now()
		code, out, err := s.runWorker(ctx)
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil:
			// The exe could not be started at all (deleted, blocked by AV).
			s.recordCrash(fmt.Sprintf("could not start the server process: %v", err), nil, -1)
		case code == workerExitQuit:
			quit() // "Quit" on the dashboard
			return
		case code == workerExitUninstall || code == workerExitUninstallData:
			removeData := code == workerExitUninstallData
			s.mu.Lock()
			s.uninstall = &removeData
			s.mu.Unlock()
			quit()
			return
		default:
			s.recordCrash(crashSummary(out, code), out, code)
		}
		if time.Since(began) > time.Minute {
			backoff = s.minBackoff // it ran fine for a while: restart promptly
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > s.maxBackoff {
			backoff = s.maxBackoff
		}
	}
}

// runWorker starts one worker and waits for it.
func (s *supervisor) runWorker(ctx context.Context) (int, []byte, error) {
	out := &tailBuffer{max: 256 << 10}
	env := os.Environ()
	if _, crash, when := s.state(); crash != "" {
		env = append(env, LastCrashEnv+"="+when.Format("Jan 2 15:04:05")+": "+crash)
	}
	p, err := s.start(env, out)
	if err != nil {
		return -1, nil, err
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	exited := make(chan int, 1)
	go func() { exited <- p.Wait() }()
	var code int
	select {
	case code = <-exited:
	case <-ctx.Done():
		// Quitting: the worker sees the quit event too. Give it time to
		// restore the display and close router ports, then force it.
		select {
		case code = <-exited:
		case <-time.After(s.grace):
			p.Kill()
			code = <-exited
		}
	}
	return code, out.bytes(), nil
}

func (s *supervisor) recordCrash(summary string, output []byte, code int) {
	now := time.Now()
	s.mu.Lock()
	s.lastCrash, s.lastCrashT = summary, now
	s.restarts++
	n := s.restarts
	s.mu.Unlock()

	_ = os.MkdirAll(s.logDir, 0o700)
	path := filepath.Join(s.logDir, "crash.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 4<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "===== %s: server process exited with code %d (restart #%d); restarting =====\n%s\n",
		now.Format(time.RFC3339), code, n, summary)
	if len(output) > 0 {
		_, _ = f.Write(output)
		_, _ = f.WriteString("\n")
	}
}

// crashSummary picks the most telling line of a crashed worker's output.
func crashSummary(out []byte, code int) string {
	for _, prefix := range []string{"panic: ", "fatal error: ", "Exception ", "runtime: "} {
		if i := bytes.Index(out, []byte(prefix)); i >= 0 {
			line := out[i:]
			if j := bytes.IndexByte(line, '\n'); j >= 0 {
				line = line[:j]
			}
			if len(line) > 300 {
				line = line[:300]
			}
			return strings.TrimSpace(string(line))
		}
	}
	return fmt.Sprintf("server process exited unexpectedly (code %#x)", uint32(code))
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

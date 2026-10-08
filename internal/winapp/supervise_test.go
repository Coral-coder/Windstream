package winapp

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The test binary doubles as the worker: with SUPERVISE_HELPER set it acts
// out one scripted outcome instead of running tests.
func TestMain(m *testing.M) {
	if action := os.Getenv("SUPERVISE_HELPER"); action != "" {
		helper(action)
		return
	}
	os.Exit(m.Run())
}

func helper(action string) {
	if f := os.Getenv("SUPERVISE_ENV_OUT"); f != "" {
		_ = os.WriteFile(f, []byte(os.Getenv(LastCrashEnv)), 0o600)
	}
	switch action {
	case "panic": // an unrecovered panic in a background goroutine
		done := make(chan struct{})
		go func() { panic("boom in a goroutine") }()
		<-done
	case "fatal": // unrecoverable runtime error
		m := map[int]int{}
		for i := 0; i < 8; i++ {
			go func() {
				for j := 0; ; j++ {
					m[j] = j
				}
			}()
		}
		time.Sleep(10 * time.Second)
	case "quit":
		os.Exit(workerExitQuit)
	case "uninstall":
		os.Exit(workerExitUninstallData)
	case "hang":
		time.Sleep(time.Hour)
	}
}

type testProc struct{ cmd *exec.Cmd }

func (p *testProc) Wait() int {
	_ = p.cmd.Wait()
	return p.cmd.ProcessState.ExitCode()
}
func (p *testProc) Kill() { _ = p.cmd.Process.Kill() }

func scripted(t *testing.T, actions ...string) (*supervisor, *atomic.Int32, string) {
	dir := t.TempDir()
	var n atomic.Int32
	s := &supervisor{logDir: dir, grace: 500 * time.Millisecond,
		minBackoff: 10 * time.Millisecond, maxBackoff: 50 * time.Millisecond}
	s.start = func(env []string, out io.Writer) (workerProc, error) {
		i := int(n.Add(1)) - 1
		action := actions[len(actions)-1]
		if i < len(actions) {
			action = actions[i]
		}
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(env, "SUPERVISE_HELPER="+action,
			"SUPERVISE_ENV_OUT="+filepath.Join(dir, "env"+strconv.Itoa(i)))
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return &testProc{cmd}, nil
	}
	return s, &n, dir
}

func TestSupervisorRestartsCrashedWorker(t *testing.T) {
	s, starts, dir := scripted(t, "panic", "fatal", "quit")
	quit := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.run(ctx, func() { close(quit) })
	select {
	case <-quit:
	default:
		t.Fatal("a worker exiting 0 (dashboard Quit) must quit the supervisor")
	}
	if starts.Load() != 3 {
		t.Fatalf("worker started %d times, want 3", starts.Load())
	}
	log, _ := os.ReadFile(filepath.Join(dir, "crash.log"))
	for _, want := range []string{"panic: boom in a goroutine", "fatal error: concurrent map writes", "restart #2"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("crash.log lacks %q:\n%.2000s", want, log)
		}
	}
	// The replacement worker learns why its predecessor died.
	if env, _ := os.ReadFile(filepath.Join(dir, "env1")); !strings.Contains(string(env), "panic: boom") {
		t.Errorf("second worker got LastCrash %q", env)
	}
	if env, _ := os.ReadFile(filepath.Join(dir, "env0")); len(env) != 0 {
		t.Errorf("first worker got LastCrash %q", env)
	}
}

func TestSupervisorUninstall(t *testing.T) {
	s, _, _ := scripted(t, "uninstall")
	quitCalled := false
	s.run(context.Background(), func() { quitCalled = true })
	if u := s.uninstallRequested(); !quitCalled || u == nil || !*u {
		t.Fatalf("uninstall not requested (quit=%v, u=%v)", quitCalled, u)
	}
}

func TestSupervisorKillsHungWorkerOnQuit(t *testing.T) {
	s, _, _ := scripted(t, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.run(ctx, func() {}); close(done) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not kill a hung worker after the grace period")
	}
}

func TestCrashSummary(t *testing.T) {
	if got := crashSummary([]byte("noise\npanic: runtime error: index out of range\n\ngoroutine 1"), 2); got != "panic: runtime error: index out of range" {
		t.Errorf("got %q", got)
	}
	if got := crashSummary(nil, -1073741819); !strings.Contains(got, "0xc0000005") {
		t.Errorf("native crash summary %q", got)
	}
	tb := &tailBuffer{max: 4, headMax: 3}
	tb.Write([]byte("abcdefghi"))
	tb.Write([]byte("jk"))
	if got := string(tb.bytes()); got != "abc\n[... output trimmed ...]\nhijk" {
		t.Errorf("head+tail = %q", got)
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.11", "1.0.9", true},
		{"1.0.9", "1.0.11", false},
		{"1.0.11", "1.0.11", false},
		{"1.1", "1.0.99", true},
		{"v2.0.0", "1.9.9", true},
		{"1.0.0", "1.0", false},
		{"dev", "1.0.0", false},
		{"1.0.0", "dev", false},
		{"", "1.0.0", false},
		{"20250417", "1.0.12", false}, // an all-digit commit hash is not a release
		{"1.0.12", "20250417", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSupervisorReportsRepeatedFailures(t *testing.T) {
	s, _, _ := scripted(t, "panic", "panic", "panic", "panic", "panic", "quit")
	var calls atomic.Int32
	var got string
	s.onRepeatedFailure = func(summary string) { calls.Add(1); got = summary }
	s.run(context.Background(), func() {})
	if calls.Load() != 1 || !strings.Contains(got, "panic: boom") {
		t.Fatalf("repeated-failure notice: calls=%d summary=%q", calls.Load(), got)
	}
}

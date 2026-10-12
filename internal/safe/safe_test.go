package safe

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecover(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, nil))
	done := make(chan struct{})
	Go(log, "test goroutine", func() {
		defer close(done)
		var m map[string]int
		m["boom"] = 1 // nil map write panics
	})
	<-done
	if !Call(log, "test call", func() { panic("kaboom") }) {
		t.Fatal("Call did not report the panic")
	}
	if Call(log, "fine", func() {}) {
		t.Fatal("Call reported a panic that did not happen")
	}
	func() {
		defer Recover(nil, "nil logger")
		panic("x")
	}()
	// The goroutine's own defers (closing done) run before Go's recover
	// logs the panic, so wait for the line rather than assume it is there.
	var out string
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		mu.Lock()
		out = buf.String()
		mu.Unlock()
		if strings.Contains(out, "test goroutine") {
			break
		}
	}
	if !strings.Contains(out, "test goroutine") || !strings.Contains(out, "kaboom") {
		t.Fatalf("panics not logged: %s", out)
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

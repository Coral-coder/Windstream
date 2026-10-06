package control

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestSafegoRecovers(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(&syncWriter{w: &buf, mu: &mu}, nil))
	done := make(chan struct{})
	// A panicking background task must not crash the test process.
	safego(log, "unit", func() { defer close(done); panic("kaboom") })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("task never ran")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !bytes.Contains([]byte(out), []byte("kaboom")) || !bytes.Contains([]byte(out), []byte("unit")) {
		t.Fatalf("panic not logged: %q", out)
	}
}

type syncWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

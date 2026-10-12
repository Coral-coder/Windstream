package control

import (
	"testing"
	"time"

	"github.com/coral-coder/windstream/internal/server"
)

func TestSignInLog(t *testing.T) {
	dir := t.TempDir()
	l := newSignInLog(dir)
	now := time.Now()
	l.add(server.SignIn{Time: now, User: "player", IP: "192.168.1.5", Result: "ok"})
	for i := 0; i < 30; i++ { // someone guessing: one entry, not thirty
		l.add(server.SignIn{Time: now.Add(time.Duration(i) * time.Second), User: "admin", IP: "203.0.113.9", Result: "failed"})
	}
	got := l.recent()
	if len(got) != 2 || got[0].Count != 30 || got[0].IP != "203.0.113.9" || got[1].User != "player" {
		t.Fatalf("recent = %+v", got)
	}
	// Survives a restart.
	if again := newSignInLog(dir).recent(); len(again) != 2 || again[0].Count != 30 {
		t.Fatalf("after reload: %+v", again)
	}
	for i := 0; i < 80; i++ {
		l.add(server.SignIn{Time: now, User: "player", IP: "192.168.1.5", Result: "ok"})
	}
	if n := len(l.recent()); n != maxSignIns {
		t.Fatalf("kept %d entries", n)
	}
}

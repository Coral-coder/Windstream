package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSessionsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	a := NewSessionStore(time.Hour, 24*time.Hour)
	if err := a.Persist(path); err != nil {
		t.Fatal(err)
	}
	tok, err := a.Create("alice", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	gone, _ := a.Create("bob", "1.2.3.5")
	a.Revoke(gone)
	if data, _ := os.ReadFile(path); strings.Contains(string(data), tok) {
		t.Fatal("the token itself was saved")
	}
	if st, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("saved file: %v %v", st, err)
	}

	b := NewSessionStore(time.Hour, 24*time.Hour) // after a restart
	if err := b.Persist(path); err != nil {
		t.Fatal(err)
	}
	if s, ok := b.Get(tok); !ok || s.User != "alice" {
		t.Errorf("signed out by the restart: %+v %v", s, ok)
	}
	if _, ok := b.Get(gone); ok {
		t.Error("a revoked session came back")
	}

	// Sessions that expired while Windstream was stopped are not loaded.
	c := NewSessionStore(time.Hour, 24*time.Hour)
	c.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if err := c.Persist(path); err != nil {
		t.Fatal(err)
	}
	if c.Count() != 0 {
		t.Error("an expired session was loaded")
	}
}

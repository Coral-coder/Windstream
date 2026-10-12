package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/coral-coder/windstream/internal/server"
)

// signInLog keeps the latest sign-in attempts for the dashboard ("who
// signed in?"), saved to disk so they survive restarts.
type signInLog struct {
	path string
	mu   sync.Mutex
	list []server.SignIn // oldest first
}

const maxSignIns = 50

func newSignInLog(dataDir string) *signInLog {
	l := &signInLog{path: filepath.Join(dataDir, "signins.json")}
	if b, err := os.ReadFile(l.path); err == nil {
		_ = json.Unmarshal(b, &l.list)
	}
	return l
}

func (l *signInLog) add(s server.SignIn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// A burst of failures from one address (someone guessing) is one
	// entry with a count, so it cannot push real sign-ins off the list.
	if n := len(l.list); n > 0 {
		last := &l.list[n-1]
		if s.Result != "ok" && last.Result == s.Result && last.IP == s.IP && last.User == s.User &&
			s.Time.Sub(last.Time) < 10*time.Minute {
			last.Time = s.Time
			last.Count++
			l.saveLocked()
			return
		}
	}
	s.Count = 1
	l.list = append(l.list, s)
	if len(l.list) > maxSignIns {
		l.list = append([]server.SignIn(nil), l.list[len(l.list)-maxSignIns:]...)
	}
	l.saveLocked()
}

func (l *signInLog) saveLocked() {
	b, err := json.Marshal(l.list)
	if err != nil {
		return
	}
	tmp := l.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, l.path)
	}
}

// recent returns the attempts newest first.
func (l *signInLog) recent() []server.SignIn {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]server.SignIn, len(l.list))
	for i, s := range l.list {
		out[len(l.list)-1-i] = s
	}
	return out
}

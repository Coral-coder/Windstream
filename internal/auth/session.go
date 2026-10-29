package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Session is an authenticated browser session.
type Session struct {
	User      string
	IP        string
	CreatedAt time.Time
	LastSeen  time.Time
}

// SessionStore keeps sessions keyed by the SHA-256 of the bearer token, so
// a memory dump or the saved file never yields usable cookies.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	idle     time.Duration
	maxAge   time.Duration
	now      func() time.Time
	path     string // where sessions are saved ("" = memory only)
}

// NewSessionStore creates a store with the given idle and absolute lifetimes.
func NewSessionStore(idle, maxAge time.Duration) *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
		idle:     idle,
		maxAge:   maxAge,
		now:      time.Now,
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Create issues a new 256-bit random token for user.
func (s *SessionStore) Create(user, ip string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	s.mu.Lock()
	s.sessions[hashToken(token)] = &Session{User: user, IP: ip, CreatedAt: now, LastSeen: now}
	s.saveLocked()
	s.mu.Unlock()
	return token, nil
}

// Persist keeps sessions in path (token hashes only, owner-readable), so
// people stay signed in across a restart, an update in particular. It
// loads the sessions saved there that are still valid.
func (s *SessionStore) Persist(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved map[string]*Session
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}
	now := s.now()
	for k, v := range saved {
		if v != nil && len(k) == 64 && now.Sub(v.LastSeen) <= s.idle && now.Sub(v.CreatedAt) <= s.maxAge {
			s.sessions[k] = v
		}
	}
	return nil
}

// Flush saves the sessions (with their last-seen times) now.
func (s *SessionStore) Flush() {
	s.mu.Lock()
	s.saveLocked()
	s.mu.Unlock()
}

func (s *SessionStore) saveLocked() {
	if s.path == "" {
		return
	}
	data, err := json.Marshal(s.sessions)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".sessions-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	_ = os.Chmod(tmp.Name(), 0o600)
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		_ = os.Remove(tmp.Name())
	}
}

// Get validates a token, refreshes its idle timer and returns the session.
func (s *SessionStore) Get(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	key := hashToken(token)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[key]
	if !ok {
		return Session{}, false
	}
	if now.Sub(sess.LastSeen) > s.idle || now.Sub(sess.CreatedAt) > s.maxAge {
		delete(s.sessions, key)
		return Session{}, false
	}
	sess.LastSeen = now
	return *sess, true
}

// Revoke deletes a session.
func (s *SessionStore) Revoke(token string) {
	s.mu.Lock()
	delete(s.sessions, hashToken(token))
	s.saveLocked()
	s.mu.Unlock()
}

// RevokeUser deletes every session belonging to user.
func (s *SessionStore) RevokeUser(user string) {
	s.mu.Lock()
	for k, v := range s.sessions {
		if v.User == user {
			delete(s.sessions, k)
		}
	}
	s.saveLocked()
	s.mu.Unlock()
}

// Sweep drops expired sessions. Call periodically.
func (s *SessionStore) Sweep() {
	now := s.now()
	s.mu.Lock()
	for k, v := range s.sessions {
		if now.Sub(v.LastSeen) > s.idle || now.Sub(v.CreatedAt) > s.maxAge {
			delete(s.sessions, k)
		}
	}
	s.saveLocked() // also records last-seen times
	s.mu.Unlock()
}

// Count returns the number of live sessions.
func (s *SessionStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

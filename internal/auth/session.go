package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

// SessionStore keeps sessions in memory keyed by the SHA-256 of the bearer
// token, so a memory dump never yields usable cookies.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	idle     time.Duration
	maxAge   time.Duration
	now      func() time.Time
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
	s.mu.Unlock()
	return token, nil
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
	s.mu.Unlock()
}

// Count returns the number of live sessions.
func (s *SessionStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

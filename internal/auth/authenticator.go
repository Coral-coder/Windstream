package auth

import (
	"sync"
	"time"
)

// User is a configured account.
type User struct {
	Name         string
	PasswordHash string
	TOTPSecret   string
}

// Result classifies a login attempt.
type Result int

const (
	// Denied means the credentials were rejected (no detail is given to clients).
	Denied Result = iota
	// TOTPRequired means the password was correct but a second factor is needed.
	TOTPRequired
	// OK means the login succeeded.
	OK
)

// Authenticator verifies passwords and second factors.
type Authenticator struct {
	umu         sync.RWMutex
	users       map[string]User
	requireTOTP bool
	mu          sync.Mutex
	lastCounter map[string]int64 // replay protection for TOTP codes
	now         func() time.Time
}

// NewAuthenticator builds an authenticator from the configured users.
func NewAuthenticator(users []User, requireTOTP bool) *Authenticator {
	m := make(map[string]User, len(users))
	for _, u := range users {
		m[u.Name] = u
	}
	return &Authenticator{users: m, requireTOTP: requireTOTP, lastCounter: make(map[string]int64), now: time.Now}
}

// SetUsers replaces the account list (live, without dropping sessions).
func (a *Authenticator) SetUsers(users []User, requireTOTP bool) {
	m := make(map[string]User, len(users))
	for _, u := range users {
		m[u.Name] = u
	}
	a.umu.Lock()
	a.users, a.requireTOTP = m, requireTOTP
	a.umu.Unlock()
}

// Count returns the number of accounts.
func (a *Authenticator) Count() int {
	a.umu.RLock()
	defer a.umu.RUnlock()
	return len(a.users)
}

// Authenticate checks username/password and, when configured, a TOTP code.
func (a *Authenticator) Authenticate(username, password, code string) Result {
	a.umu.RLock()
	u, ok := a.users[username]
	requireTOTP := a.requireTOTP
	a.umu.RUnlock()
	if !ok {
		BurnVerify(password)
		return Denied
	}
	match, err := VerifyPassword(u.PasswordHash, password)
	if err != nil || !match {
		return Denied
	}
	if u.TOTPSecret == "" {
		if requireTOTP {
			return Denied
		}
		return OK
	}
	if code == "" {
		return TOTPRequired
	}
	counter, ok := ValidateTOTP(u.TOTPSecret, code, a.now(), 1)
	if !ok {
		return Denied
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if counter <= a.lastCounter[username] {
		return Denied // replayed or older code
	}
	a.lastCounter[username] = counter
	return OK
}

// HasUser reports whether username is configured.
func (a *Authenticator) HasUser(username string) bool {
	a.umu.RLock()
	defer a.umu.RUnlock()
	_, ok := a.users[username]
	return ok
}

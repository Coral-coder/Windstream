// Package auth implements password hashing, TOTP, sessions and login rate
// limiting for Windstream.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. 64 MiB / 3 passes / 4 lanes is the OWASP-recommended
// interactive profile and takes ~100ms on a modern CPU, which combined with
// per-IP rate limiting makes online guessing impractical.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

var (
	// ErrMalformedHash is returned for hashes that are not argon2id PHC strings.
	ErrMalformedHash = errors.New("auth: malformed argon2id hash")

	dummyOnce sync.Once
	dummyHash string
)

// HashPassword derives an argon2id PHC string from a password.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("auth: empty password")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against an argon2id PHC string in constant
// time relative to the hash contents.
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrMalformedHash
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, ErrMalformedHash
	}
	if m < 8*1024 || t < 1 || p < 1 || m > 4*1024*1024 || t > 16 {
		return false, ErrMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, ErrMalformedHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 {
		return false, ErrMalformedHash
	}
	verifySlots <- struct{}{}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	<-verifySlots
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// verifySlots bounds concurrent verifications: each one takes 64 MB and a
// core for a moment, and a burst of logins (from many addresses at once)
// must not be able to exhaust the PC's memory while it is running a game.
var verifySlots = make(chan struct{}, 2)

// BurnVerify spends the same CPU time as a real verification. It is called
// when a login names an unknown user so response timing does not reveal which
// usernames exist.
func BurnVerify(password string) {
	dummyOnce.Do(func() {
		h, err := HashPassword("windstream-timing-equaliser")
		if err == nil {
			dummyHash = h
		}
	})
	if dummyHash != "" {
		_, _ = VerifyPassword(dummyHash, password)
	}
}

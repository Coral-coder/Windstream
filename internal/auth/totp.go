package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 mandates HMAC-SHA1 for interoperable TOTP.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpPeriod = 30 * time.Second
	totpDigits = 6
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a new 160-bit base32 secret.
func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b32.EncodeToString(buf), nil
}

func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(secret))
	key, err := b32.DecodeString(s)
	if err != nil || len(key) < 10 {
		return nil, errors.New("auth: invalid TOTP secret")
	}
	return key, nil
}

// TOTPCode computes the RFC 6238 code for the given counter.
func totpAt(key []byte, counter int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, code%1000000)
}

// TOTPCode returns the code valid at time t. Useful for setup verification.
func TOTPCode(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return totpAt(key, t.Unix()/int64(totpPeriod.Seconds())), nil
}

// ValidateTOTP checks code against the secret allowing ±skew periods. It
// returns the matched counter so callers can reject replays.
func ValidateTOTP(secret, code string, now time.Time, skew int) (int64, bool) {
	key, err := decodeSecret(secret)
	if err != nil {
		return 0, false
	}
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	counter := now.Unix() / int64(totpPeriod.Seconds())
	matched := int64(-1)
	for d := -skew; d <= skew; d++ {
		c := counter + int64(d)
		if subtle.ConstantTimeCompare([]byte(totpAt(key, c)), []byte(code)) == 1 && matched < 0 {
			matched = c
		}
	}
	return matched, matched >= 0
}

// TOTPURL renders an otpauth:// URL for authenticator apps.
func TOTPURL(issuer, account, secret string) string {
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}

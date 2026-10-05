package auth

import (
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword(h, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify good: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(h, "wrong")
	if err != nil || ok {
		t.Fatalf("verify bad: ok=%v err=%v", ok, err)
	}
	if _, err := VerifyPassword("$argon2i$junk", "x"); err == nil {
		t.Fatal("expected malformed hash error")
	}
}

func TestTOTPKnownVector(t *testing.T) {
	// RFC 6238 Appendix B vector (SHA1, secret "12345678901234567890").
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	code, err := TOTPCode(secret, time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("code = %s, want 287082", code)
	}
	if _, ok := ValidateTOTP(secret, "287082", time.Unix(59, 0), 1); !ok {
		t.Fatal("validate failed")
	}
	if _, ok := ValidateTOTP(secret, "000000", time.Unix(59, 0), 1); ok {
		t.Fatal("validate accepted wrong code")
	}
}

func TestAuthenticatorFlow(t *testing.T) {
	h, _ := HashPassword("pw")
	secret, _ := GenerateTOTPSecret()
	a := NewAuthenticator([]User{
		{Name: "plain", PasswordHash: h},
		{Name: "mfa", PasswordHash: h, TOTPSecret: secret},
	}, false)
	now := time.Now()
	a.now = func() time.Time { return now }

	if got := a.Authenticate("nobody", "pw", ""); got != Denied {
		t.Errorf("unknown user: %v", got)
	}
	if got := a.Authenticate("plain", "bad", ""); got != Denied {
		t.Errorf("bad password: %v", got)
	}
	if got := a.Authenticate("plain", "pw", ""); got != OK {
		t.Errorf("plain login: %v", got)
	}
	if got := a.Authenticate("mfa", "pw", ""); got != TOTPRequired {
		t.Errorf("mfa without code: %v", got)
	}
	code, _ := TOTPCode(secret, now)
	if got := a.Authenticate("mfa", "pw", code); got != OK {
		t.Errorf("mfa with code: %v", got)
	}
	if got := a.Authenticate("mfa", "pw", code); got != Denied {
		t.Errorf("replayed code accepted: %v", got)
	}
}

func TestSessionLifetimes(t *testing.T) {
	s := NewSessionStore(time.Minute, time.Hour)
	now := time.Now()
	s.now = func() time.Time { return now }
	tok, err := s.Create("alice", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(tok); !ok {
		t.Fatal("fresh session missing")
	}
	now = now.Add(61 * time.Second)
	if _, ok := s.Get(tok); ok {
		t.Fatal("idle session should expire")
	}
	tok, _ = s.Create("alice", "127.0.0.1")
	for i := 0; i < 70; i++ {
		now = now.Add(55 * time.Second)
		s.Get(tok)
	}
	if _, ok := s.Get(tok); ok {
		t.Fatal("session should hit absolute max age")
	}
	if _, ok := s.Get("not-a-token"); ok {
		t.Fatal("bogus token accepted")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !l.Allow("ip") {
			t.Fatalf("attempt %d denied", i)
		}
	}
	if l.Allow("ip") {
		t.Fatal("4th attempt allowed")
	}
	if !l.Allow("other") {
		t.Fatal("other key affected")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allow("ip") {
		t.Fatal("window did not reset")
	}
}

package deps

import (
	"strings"
	"testing"
)

func TestGuardRecoversPanic(t *testing.T) {
	err := guard(func() error { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("guard did not convert panic to error: %v", err)
	}
	if err := guard(func() error { return nil }); err != nil {
		t.Fatalf("guard altered success: %v", err)
	}
}

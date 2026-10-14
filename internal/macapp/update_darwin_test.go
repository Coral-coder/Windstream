//go:build darwin

package macapp

import (
	"os"
	"strings"
	"testing"
)

// CI signs two bundles (see the signing check in ci.yml) and points these
// variables at them.
func TestDesignatedRequirement(t *testing.T) {
	signed, adhoc := os.Getenv("WINDSTREAM_TEST_SIGNED_APP"), os.Getenv("WINDSTREAM_TEST_ADHOC_APP")
	if signed == "" || adhoc == "" {
		t.Skip("set WINDSTREAM_TEST_SIGNED_APP and WINDSTREAM_TEST_ADHOC_APP")
	}
	if dr := designatedRequirement(signed); !strings.Contains(dr, "certificate leaf") {
		t.Errorf("certificate-signed app: requirement %q", dr)
	}
	if dr := designatedRequirement(adhoc); dr != "" {
		t.Errorf("ad-hoc app has requirement %q; it must not count as a lasting identity", dr)
	}
}

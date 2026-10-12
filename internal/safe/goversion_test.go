package safe

import (
	"runtime"
	"testing"
)

// Go 1.26.0 and 1.26.1 corrupt memory in Windows network reads
// (golang/go#77975, fixed in 1.26.2): Windows writes the sender address
// length into a variable that no longer exists, and Windstream crashed with
// "fatal error: fault" after minutes of streaming. Never ship a build made
// with them.
func TestNotBuiltWithBrokenGo(t *testing.T) {
	switch v := runtime.Version(); v {
	case "go1.26.0", "go1.26.1":
		t.Fatalf("%s corrupts memory on Windows (golang/go#77975); build with go1.26.2 or later", v)
	}
}

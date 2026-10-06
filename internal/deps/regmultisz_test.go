package deps

import (
	"bytes"
	"testing"
)

func TestRegMultiSZ(t *testing.T) {
	// "Root\MttVDD" + NUL + NUL, UTF-16LE.
	got, err := regMultiSZ(VDDHardwareID)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 0, 26)
	for _, r := range VDDHardwareID {
		want = append(want, byte(r), 0)
	}
	want = append(want, 0, 0, 0, 0) // entry terminator + list terminator
	if !bytes.Equal(got, want) {
		t.Fatalf("regMultiSZ(%q) = % x\nwant % x", VDDHardwareID, got, want)
	}
	if len(got) != (len(VDDHardwareID)+2)*2 {
		t.Fatalf("length %d", len(got))
	}
	// Embedded NUL must be rejected, not panic.
	if _, err := regMultiSZ("bad\x00id"); err == nil {
		t.Fatal("embedded NUL accepted")
	}
}

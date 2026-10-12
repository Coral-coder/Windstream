package macapp

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestBundlePath(t *testing.T) {
	b, ok := BundlePath("/Applications/Windstream.app/Contents/MacOS/Windstream")
	if !ok || b != "/Applications/Windstream.app" {
		t.Fatalf("got %q %v", b, ok)
	}
	// A translocated copy (launched straight from Downloads) is a bundle too.
	if b, ok := BundlePath("/private/var/folders/x/AppTranslocation/ABC/d/Windstream.app/Contents/MacOS/Windstream"); !ok || !strings.HasSuffix(b, "/d/Windstream.app") {
		t.Fatalf("translocated: %q %v", b, ok)
	}
	for _, exe := range []string{"/usr/local/bin/windstream", "/tmp/Contents/MacOS/x"} {
		if _, ok := BundlePath(exe); ok {
			t.Errorf("%s taken for a bundle", exe)
		}
	}
}

func TestPlistsAreWellFormed(t *testing.T) {
	for name, doc := range map[string]string{
		"agent": AgentPlist("/Applications/Windstream.app/Contents/MacOS/Windstream", "/Users/a b/Library/Application Support/Windstream/logs"),
		"info":  InfoPlist("1.0.15"),
	} {
		d := xml.NewDecoder(strings.NewReader(doc))
		d.Strict = false
		for {
			if _, err := d.Token(); err != nil {
				if err.Error() != "EOF" {
					t.Errorf("%s: %v", name, err)
				}
				break
			}
		}
	}
	a := AgentPlist("/Applications/Windstream.app/Contents/MacOS/Windstream", "/l")
	for _, want := range []string{"<string>--agent</string>", "<key>SuccessfulExit</key><false/>", "RunAtLoad", Label} {
		if !strings.Contains(a, want) {
			t.Errorf("agent plist lacks %q", want)
		}
	}
	if !strings.Contains(InfoPlist("1.2.3"), "<key>LSUIElement</key><true/>") {
		t.Error("app would show a Dock icon")
	}
}

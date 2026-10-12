// Package macapp is Windstream as a Mac app: Windstream.app installs itself
// into Applications, runs the streaming server as a login item (a launchd
// agent that macOS restarts if it ever crashes), and opens the dashboard
// when double-clicked. It has no menu-bar icon; the dashboard has Quit and
// Uninstall.
package macapp

import (
	"fmt"
	"html"
	"path"
	"strings"
)

// Identifiers macOS knows the app and its login item by.
const (
	BundleID = "com.windstream.app"
	Label    = "com.windstream.agent"
	AppName  = "Windstream.app"
)

// BundlePath returns the .app bundle that contains executable exe.
func BundlePath(exe string) (string, bool) {
	// macOS paths (slash-separated) whatever OS this is compiled for.
	macOSDir := path.Dir(exe)
	contents := path.Dir(macOSDir)
	bundle := path.Dir(contents)
	if path.Base(macOSDir) != "MacOS" || path.Base(contents) != "Contents" || !strings.HasSuffix(bundle, ".app") {
		return "", false
	}
	return bundle, true
}

// AgentPlist is the launchd definition that runs exe as the server at login
// and restarts it if it crashes (but not after Quit, which exits cleanly).
func AgentPlist(exe, logDir string) string {
	esc := func(s string) string { return html.EscapeString(s) }
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>--agent</string></array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key>
	<dict><key>SuccessfulExit</key><false/></dict>
	<key>ThrottleInterval</key><integer>5</integer>
	<key>ProcessType</key><string>Interactive</string>
	<key>LimitLoadToSessionType</key><string>Aqua</string>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, Label, esc(exe), esc(path.Join(logDir, "agent.log")), esc(path.Join(logDir, "agent.log")))
}

// InfoPlist is the app bundle's Info.plist.
func InfoPlist(version string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>Windstream</string>
	<key>CFBundleDisplayName</key><string>Windstream</string>
	<key>CFBundleIdentifier</key><string>%s</string>
	<key>CFBundleExecutable</key><string>Windstream</string>
	<key>CFBundleIconFile</key><string>Windstream</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>%s</string>
	<key>CFBundleVersion</key><string>%s</string>
	<key>LSMinimumSystemVersion</key><string>12.0</string>
	<key>LSUIElement</key><true/>
	<key>NSHighResolutionCapable</key><true/>
	<key>NSScreenCaptureUsageDescription</key><string>Windstream streams this Mac's screen to your other devices.</string>
	<key>NSAppleEventsUsageDescription</key><string>Windstream shows messages while installing.</string>
</dict>
</plist>
`, BundleID, html.EscapeString(version), html.EscapeString(version))
}

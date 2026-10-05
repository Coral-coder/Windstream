package deps

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// VDDConfigDir is where the Virtual Display Driver reads vdd_settings.xml
// (its compiled-in default).
const VDDConfigDir = `C:\VirtualDisplayDriver`

// VDDSettingsXML renders the driver configuration: one monitor, the GPU
// chosen by Windows, and a mode list that always contains the requested mode.
func VDDSettingsXML(width, height, refresh int) string {
	type res struct{ w, h int }
	modes := []res{{1280, 720}, {1600, 900}, {1920, 1080}, {2560, 1440}, {3440, 1440}, {3840, 2160}}
	found := false
	for _, m := range modes {
		if m.w == width && m.h == height {
			found = true
		}
	}
	if !found && width > 0 && height > 0 {
		modes = append(modes, res{width, height})
	}
	rates := map[int]bool{30: true, 60: true, 90: true, 120: true, 144: true, 165: true, 240: true}
	if refresh > 0 {
		rates[refresh] = true
	}
	var rlist []int
	for r := range rates {
		rlist = append(rlist, r)
	}
	sort.Ints(rlist)

	var b strings.Builder
	b.WriteString("<?xml version='1.0' encoding='utf-8'?>\r\n<vdd_settings>\r\n")
	b.WriteString("    <monitors>\r\n        <count>1</count>\r\n    </monitors>\r\n")
	b.WriteString("    <gpu>\r\n        <friendlyname>default</friendlyname>\r\n    </gpu>\r\n")
	b.WriteString("    <global>\r\n")
	for _, r := range rlist {
		fmt.Fprintf(&b, "        <g_refresh_rate>%d</g_refresh_rate>\r\n", r)
	}
	b.WriteString("    </global>\r\n    <resolutions>\r\n")
	for _, m := range modes {
		fmt.Fprintf(&b, "        <resolution>\r\n            <width>%d</width>\r\n            <height>%d</height>\r\n            <refresh_rate>60</refresh_rate>\r\n        </resolution>\r\n", m.w, m.h)
	}
	b.WriteString("    </resolutions>\r\n    <options>\r\n")
	for _, kv := range [][2]string{{"CustomEdid", "false"}, {"PreventSpoof", "false"}, {"EdidCeaOverride", "false"},
		{"HardwareCursor", "true"}, {"SDR10bit", "false"}, {"HDRPlus", "false"}, {"logging", "false"}, {"debuglogging", "false"}} {
		fmt.Fprintf(&b, "        <%s>%s</%s>\r\n", kv[0], kv[1], kv[0])
	}
	b.WriteString("    </options>\r\n</vdd_settings>\r\n")
	return b.String()
}

// WriteVDDSettings writes the driver configuration if it changed and
// reports whether it did.
func WriteVDDSettings(dir string, width, height, refresh int) (bool, error) {
	content := VDDSettingsXML(width, height, refresh)
	path := filepath.Join(dir, "vdd_settings.xml")
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(content), 0o644)
}

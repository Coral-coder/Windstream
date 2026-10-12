package media

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var avfDeviceLine = regexp.MustCompile(`\]\s*\[(\d+)\]\s*(.+?)\s*$`)

// parseAVFoundationDevices reads ffmpeg's `-f avfoundation -list_devices
// true` output into its video and audio device names.
func parseAVFoundationDevices(out []byte) (video, audio []string) {
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, "AVFoundation video devices:"):
			section = "video"
			continue
		case strings.Contains(line, "AVFoundation audio devices:"):
			section = "audio"
			continue
		}
		m := avfDeviceLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch section {
		case "video":
			video = append(video, m[2])
		case "audio":
			audio = append(audio, m[2])
		}
	}
	return video, audio
}

// AVFoundationAudioDevices lists the Mac's audio input devices.
func AVFoundationAudioDevices(ctx context.Context, ffmpeg string) []string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// ffmpeg exits with an error after listing (there is no input); the
	// list is on stderr either way.
	out, _ := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "").CombinedOutput()
	_, audio := parseAVFoundationDevices(out)
	return audio
}

// loopbackNames are virtual audio devices that carry what the Mac plays.
var loopbackNames = []string{"blackhole", "loopback", "soundflower", "ishowu", "background music"}

// PickLoopback returns the first loopback device among devices, or "".
func PickLoopback(devices []string) string {
	for _, want := range loopbackNames {
		for _, d := range devices {
			if strings.Contains(strings.ToLower(d), want) {
				return d
			}
		}
	}
	return ""
}

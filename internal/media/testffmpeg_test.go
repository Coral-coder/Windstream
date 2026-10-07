package media

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// testFFmpeg returns the ffmpeg used by integration tests:
// $WINDSTREAM_TEST_FFMPEG, else ffmpeg from PATH.
func testFFmpeg(t *testing.T) string {
	t.Helper()
	ff := os.Getenv("WINDSTREAM_TEST_FFMPEG")
	if ff == "" {
		ff = "ffmpeg"
	}
	if _, err := exec.LookPath(ff); err != nil {
		t.Skip("ffmpeg not installed")
	}
	return ff
}

// needEncoder skips unless ffmpeg has the encoder (and, for AV1, the AV1 RTP
// packetizer added in FFmpeg 8).
func needEncoder(t *testing.T, ff, name string) {
	t.Helper()
	out, _ := exec.Command(ff, "-hide_banner", "-encoders").Output()
	if !strings.Contains(string(out), " "+name+" ") {
		t.Skipf("ffmpeg lacks %s", name)
	}
	if strings.Contains(name, "av1") && ffmpegMajor(ff) < 8 {
		t.Skip("ffmpeg < 8 cannot packetize AV1 over RTP (set WINDSTREAM_TEST_FFMPEG)")
	}
}

func ffmpegMajor(ff string) int {
	out, _ := exec.Command(ff, "-version").Output()
	m := regexp.MustCompile(`ffmpeg version n?(\d+)\.`).FindSubmatch(out)
	if m == nil {
		return 99 // git builds: assume new
	}
	v, _ := strconv.Atoi(string(m[1]))
	return v
}

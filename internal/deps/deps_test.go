package deps

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDownloadVerifiesHash(t *testing.T) {
	body := []byte("windstream test payload")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	dir := t.TempDir()
	good := Artifact{Name: "good", URL: srv.URL, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	var calls int
	if err := Download(context.Background(), good, filepath.Join(dir, "a"), func(d, t int64) { calls++ }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a")); string(b) != string(body) || calls == 0 {
		t.Fatalf("content/progress wrong: %q calls=%d", b, calls)
	}
	bad := good
	bad.SHA256 = strings.Repeat("0", 64)
	err := Download(context.Background(), bad, filepath.Join(dir, "b"), nil)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("expected hash mismatch, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b")); err == nil {
		t.Fatal("unverified file left in place")
	}
}

func TestExtractMatchingFlattensAndBlocksTraversal(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "x.zip")
	f, _ := os.Create(zp)
	zw := zip.NewWriter(f)
	for name, content := range map[string]string{
		"ffmpeg-9/bin/ffmpeg.exe":       "exe",
		"ffmpeg-9/bin/ffprobe.exe":      "probe",
		"../../evil/bin/ffmpeg.exe.bak": "x",
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	f.Close()
	out := filepath.Join(dir, "out")
	files, err := ExtractMatching(zp, "/bin/ffmpeg.exe", out)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range files {
		if filepath.Dir(p) != out {
			t.Fatalf("escaped dest dir: %s", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(out, "ffmpeg.exe")); string(b) != "exe" {
		t.Fatalf("ffmpeg.exe = %q", b)
	}
	if _, err := os.Stat(filepath.Join(out, "ffprobe.exe")); err == nil {
		t.Fatal("non-matching entry extracted")
	}
}

func TestVDDSettingsXML(t *testing.T) {
	x := VDDSettingsXML(2560, 1600, 75)
	var doc struct {
		Count int   `xml:"monitors>count"`
		Rates []int `xml:"global>g_refresh_rate"`
		Res   []struct {
			W int `xml:"width"`
			H int `xml:"height"`
		} `xml:"resolutions>resolution"`
	}
	if err := xml.Unmarshal([]byte(x), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Count != 1 {
		t.Errorf("monitor count %d", doc.Count)
	}
	hasRate, hasRes := false, false
	for _, r := range doc.Rates {
		hasRate = hasRate || r == 75
	}
	for _, r := range doc.Res {
		hasRes = hasRes || (r.W == 2560 && r.H == 1600)
	}
	if !hasRate || !hasRes {
		t.Errorf("requested mode missing: rates=%v res=%v", doc.Rates, doc.Res)
	}
	dir := t.TempDir()
	if changed, err := WriteVDDSettings(dir, 1920, 1080, 60); err != nil || !changed {
		t.Fatalf("first write: %v %v", changed, err)
	}
	if changed, _ := WriteVDDSettings(dir, 1920, 1080, 60); changed {
		t.Fatal("unchanged settings rewritten")
	}
}

func TestFFmpegBuildPerOS(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, want string }{
		{"windows", "amd64", FFmpeg.URL},
		{"darwin", "arm64", FFmpegMacARM64.URL},
		{"darwin", "amd64", FFmpegMacX64.URL},
		{"linux", "amd64", ""},
	} {
		art, _, _ := ffmpegBuild(tc.goos, tc.goarch)
		if art.URL != tc.want {
			t.Errorf("%s/%s: %q", tc.goos, tc.goarch, art.URL)
		}
	}
}

func TestGunzipTo(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "ffmpeg.gz")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte("#!/bin/sh\necho ffmpeg\n"))
	_ = zw.Close()
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	_ = os.MkdirAll(bin, 0o755)
	if err := gunzipTo("ffmpeg")(archive, bin); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(bin, "ffmpeg"))
	// Windows has no executable bit.
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm()&0o100 == 0) {
		t.Fatalf("not installed executable: %v %v", st, err)
	}
}

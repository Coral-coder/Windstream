package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeGitHub serves a release like GitHub does.
func fakeGitHub(t *testing.T, tag string, exe []byte, sums string) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(UseServer(srv.URL))
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://example/rel","assets":[
			{"name":"Windstream.exe","browser_download_url":"%s/download/%s/Windstream.exe","size":%d},
			{"name":"SHA256SUMS.txt","browser_download_url":"%s/download/%s/SHA256SUMS.txt","size":%d},
			{"name":"Elsewhere.exe","browser_download_url":"https://evil.example/Elsewhere.exe","size":1}]}`,
			tag, srv.URL, tag, len(exe), srv.URL, tag, len(sums))
	})
	mux.HandleFunc("/download/"+tag+"/Windstream.exe", func(w http.ResponseWriter, r *http.Request) { w.Write(exe) })
	mux.HandleFunc("/download/"+tag+"/SHA256SUMS.txt", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sums)) })
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestLatestAndFetch(t *testing.T) {
	exe := []byte("new windstream build")
	fakeGitHub(t, "v1.0.16", exe, sum([]byte("x"))+"  Windstream-arm64.exe\n"+sum(exe)+" *Windstream.exe\n")
	r, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "1.0.16" || !r.Newer("1.0.15") || r.Newer("1.0.16") || r.Newer("dev") || !r.Has("Windstream.exe") {
		t.Fatalf("release %+v", r)
	}
	if r.Has("Elsewhere.exe") {
		t.Error("a download from outside the release was accepted")
	}
	path, err := Fetch(context.Background(), r, "Windstream.exe", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(exe) {
		t.Errorf("downloaded %q", b)
	}
}

func TestFetchRejectsTamperedFile(t *testing.T) {
	fakeGitHub(t, "v1.0.16", []byte("tampered"), sum([]byte("the real build"))+"  Windstream.exe\n")
	r, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(context.Background(), r, "Windstream.exe", t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("tampered download accepted: %v", err)
	}
}

func TestFetchNeedsChecksum(t *testing.T) {
	fakeGitHub(t, "v1.0.16", []byte("build"), sum([]byte("other"))+"  Other.exe\n")
	r, _ := Latest(context.Background())
	if _, err := Fetch(context.Background(), r, "Windstream.exe", t.TempDir(), nil); err == nil {
		t.Fatal("download without a listed checksum accepted")
	}
}

func TestAssetName(t *testing.T) {
	for _, c := range [][3]string{{"windows", "amd64", "Windstream.exe"}, {"windows", "arm64", "Windstream-arm64.exe"},
		{"darwin", "arm64", "Windstream-macOS.zip"}, {"darwin", "amd64", "Windstream-macOS.zip"}, {"linux", "amd64", ""}} {
		if got := AssetName(c[0], c[1]); got != c[2] {
			t.Errorf("%s/%s: %q", c[0], c[1], got)
		}
	}
}

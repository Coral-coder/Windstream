// Package update finds and downloads new Windstream releases from the
// project's GitHub releases. Installing them is up to each platform.
package update

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/coral-coder/windstream/internal/appver"
	"github.com/coral-coder/windstream/internal/deps"
)

// Repo is where releases are published.
const Repo = "Coral-coder/Windstream"

// Overridden by tests.
var (
	latestURL      = "https://api.github.com/repos/" + Repo + "/releases/latest"
	downloadPrefix = "https://github.com/" + Repo + "/releases/download/"
	client         = &http.Client{Timeout: 30 * time.Second}
)

// sumsName is the checksum list every release carries.
const sumsName = "SHA256SUMS.txt"

// Release is a published version.
type Release struct {
	Version string `json:"version"` // without the leading "v"
	Tag     string `json:"tag"`
	Page    string `json:"page"` // release page on GitHub
	assets  map[string]asset
}

type asset struct {
	url  string
	size int64
}

// AssetName is the download for an OS/architecture, or "" if there is none.
func AssetName(goos, goarch string) string {
	switch {
	case goos == "windows" && goarch == "amd64":
		return "Windstream.exe"
	case goos == "windows" && goarch == "arm64":
		return "Windstream-arm64.exe"
	case goos == "darwin":
		return "Windstream-macOS.zip"
	}
	return ""
}

// Newer reports whether r is newer than the running version. Development
// builds ("dev") never update.
func (r Release) Newer(current string) bool {
	return current != "dev" && current != "" && r.Version != "" && appver.Newer(r.Version, current)
}

// Has reports whether the release carries the named download.
func (r Release) Has(name string) bool {
	_, ok := r.assets[name]
	return ok
}

// Latest asks GitHub for the newest release.
func Latest(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Windstream")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("check for updates: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("check for updates: GitHub answered %s", resp.Status)
	}
	var gh struct {
		TagName    string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&gh); err != nil {
		return Release{}, fmt.Errorf("check for updates: %w", err)
	}
	if gh.Draft || gh.Prerelease || gh.TagName == "" {
		return Release{}, errors.New("check for updates: no published release")
	}
	r := Release{Version: strings.TrimPrefix(gh.TagName, "v"), Tag: gh.TagName, Page: gh.HTMLURL, assets: map[string]asset{}}
	for _, a := range gh.Assets {
		// Only files published with this release, from this repository.
		if strings.HasPrefix(a.URL, downloadPrefix+gh.TagName+"/") {
			r.assets[a.Name] = asset{url: a.URL, size: a.Size}
		}
	}
	return r, nil
}

// Fetch downloads the named file of the release into dir and checks it
// against the release's SHA256SUMS.txt. It returns the file's path.
func Fetch(ctx context.Context, r Release, name, dir string, progress deps.Progress) (string, error) {
	a, ok := r.assets[name]
	if !ok {
		return "", fmt.Errorf("release %s has no %s", r.Tag, name)
	}
	sums, ok := r.assets[sumsName]
	if !ok {
		return "", fmt.Errorf("release %s has no %s", r.Tag, sumsName)
	}
	want, err := checksum(ctx, sums.url, name)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dir, r.Version+"-"+name)
	if err := deps.Download(ctx, deps.Artifact{Name: "Windstream " + r.Version, URL: a.url, SHA256: want, Size: a.size}, dest, progress); err != nil {
		return "", err
	}
	return dest, nil
}

// checksum reads name's SHA-256 from a sha256sum-style list.
func checksum(ctx context.Context, url, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Windstream")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download checksums: %s", resp.Status)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s is not listed in %s", name, sumsName)
}

// UseServer points update checks at base (a test server standing in for
// GitHub, serving /latest and /download/<tag>/<file>) and returns a
// function that restores GitHub. For tests only.
func UseServer(base string) (restore func()) {
	oldLatest, oldPrefix := latestURL, downloadPrefix
	latestURL, downloadPrefix = base+"/latest", base+"/download/"
	return func() { latestURL, downloadPrefix = oldLatest, oldPrefix }
}

package deps

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Progress reports bytes downloaded so far and the expected total.
type Progress func(done, total int64)

var httpClient = &http.Client{Timeout: 30 * time.Minute}

// Download fetches a to dest, verifying its SHA-256. A verified file already
// at dest is reused. Retries transient failures.
func Download(ctx context.Context, a Artifact, dest string, progress Progress) error {
	if ok, _ := verifyFile(dest, a.SHA256); ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = downloadOnce(ctx, a, dest, progress); err == nil {
			return nil
		}
		if ctx.Err() != nil || errors.Is(err, errHashMismatch) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt*2) * time.Second):
		}
	}
	return fmt.Errorf("download %s: %w", a.Name, err)
}

var errHashMismatch = errors.New("SHA-256 mismatch")

func downloadOnce(ctx context.Context, a Artifact, dest string, progress Progress) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Windstream")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	total := a.Size
	if resp.ContentLength > 0 {
		total = resp.ContentLength
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	var done int64
	buf := make([]byte, 256*1024)
	last := time.Time{}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			h.Write(buf[:n])
			done += int64(n)
			if progress != nil && time.Since(last) > 200*time.Millisecond {
				progress(done, total)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if progress != nil {
		progress(done, total)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		os.Remove(tmp)
		return fmt.Errorf("%s: %w (got %s)", a.Name, errHashMismatch, got)
	}
	return os.Rename(tmp, dest)
}

func verifyFile(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want), nil
}

// ExtractMatching extracts every zip entry whose slash-separated path
// contains match (e.g. "/bin/ffmpeg.exe" or "VirtualDisplayDriver/") into
// destDir, flattening directories. Entries are written via a temp file and
// renamed, and path traversal is impossible because only base names are used.
func ExtractMatching(zipPath, match, destDir string) ([]string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	var out []string
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.Contains(f.Name, match) {
			continue
		}
		name := filepath.Base(filepath.FromSlash(f.Name))
		if name == "." || name == ".." || name == "" {
			continue
		}
		dst := filepath.Join(destDir, name)
		if err := extractOne(f, dst); err != nil {
			return out, err
		}
		out = append(out, dst)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: nothing matching %q", filepath.Base(zipPath), match)
	}
	return out, nil
}

func extractOne(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	tmp := dst + ".tmp"
	w, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, rc); err != nil {
		w.Close()
		os.Remove(tmp)
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	_ = os.Remove(dst)
	return os.Rename(tmp, dst)
}

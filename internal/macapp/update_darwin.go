//go:build darwin

package macapp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coral-coder/windstream/internal/update"
)

// UpdateAsset names the release download for Macs.
func (p *platform) UpdateAsset() string { return update.AssetName(runtime.GOOS, runtime.GOARCH) }

// UnattendedUpdates reports whether this copy is signed with a certificate
// (self-signed or Developer ID). macOS ties the Screen Recording and
// Accessibility permissions to the app's signing identity: with a
// certificate, an update signed with the same one keeps them; an ad-hoc
// signed app has no lasting identity, so every update needs someone at the
// Mac to allow them again.
func (p *platform) UnattendedUpdates() bool {
	b, ok := installedBundle()
	return ok && designatedRequirement(b) != ""
}

var drCache sync.Map // bundle path -> designated requirement

// designatedRequirement is the code requirement macOS checks to decide
// whether a program is the app permissions were given to ("" when ad-hoc
// signed, which has none beyond the exact build).
func designatedRequirement(bundle string) string {
	if v, ok := drCache.Load(bundle); ok {
		return v.(string)
	}
	out, _ := exec.Command("/usr/bin/codesign", "--display", "--requirements", "-", bundle).CombinedOutput()
	dr := ""
	for _, line := range strings.Split(string(out), "\n") {
		if r, ok := strings.CutPrefix(line, "designated => "); ok && strings.Contains(r, "certificate") {
			dr = strings.TrimSpace(r)
		}
	}
	drCache.Store(bundle, dr)
	return dr
}

func installedBundle() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return BundlePath(exe)
}

// InstallUpdate replaces the installed Windstream.app with the one in the
// downloaded zip and restarts it.
func (p *platform) InstallUpdate(file, version string, unattended bool) error {
	target, ok := installedBundle()
	if !ok {
		return errors.New("Windstream is not running from an installed Windstream.app")
	}
	unpack := filepath.Join(filepath.Dir(file), "unpack-"+version)
	_ = os.RemoveAll(unpack)
	defer os.RemoveAll(unpack)
	if out, err := exec.Command("/usr/bin/ditto", "-x", "-k", file, unpack).CombinedOutput(); err != nil {
		return fmt.Errorf("unpack update: %v %s", err, out)
	}
	bundle := filepath.Join(unpack, AppName)
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", bundle).CombinedOutput(); err != nil {
		return fmt.Errorf("the downloaded app is damaged: %v %s", err, out)
	}
	keeps := false
	if dr := designatedRequirement(target); dr != "" {
		keeps = exec.Command("/usr/bin/codesign", "--verify", "-R="+dr, bundle).Run() == nil
	}
	if unattended && !keeps {
		// It would lose Screen Recording and Accessibility with nobody
		// here to allow them again: leave it to the Update now button.
		return errors.New("this update is signed with a different certificate, so macOS will ask for Screen Recording and Accessibility again: install it with Update now while you're at the Mac")
	}
	if err := copyBundle(bundle, target); err != nil {
		return err
	}
	drCache.Delete(target)
	go func() {
		time.Sleep(time.Second) // let the dashboard hear back first
		p.restart()
	}()
	return nil
}

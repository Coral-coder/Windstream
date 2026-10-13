//go:build darwin

package macapp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/coral-coder/windstream/internal/update"
)

// UpdateAsset names the release download for Macs.
func (p *platform) UpdateAsset() string { return update.AssetName(runtime.GOOS, runtime.GOARCH) }

// UnattendedUpdates is false: the app is not signed by an Apple developer
// account, so macOS treats every new version as a different app and asks
// again for Screen Recording and Accessibility. An update must happen
// while someone is at the Mac to allow them.
func (p *platform) UnattendedUpdates() bool { return false }

// InstallUpdate replaces the installed Windstream.app with the one in the
// downloaded zip and restarts it.
func (p *platform) InstallUpdate(file, version string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	target, ok := BundlePath(exe)
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
	if err := copyBundle(bundle, target); err != nil {
		return err
	}
	go func() {
		time.Sleep(time.Second) // let the dashboard hear back first
		p.restart()
	}()
	return nil
}

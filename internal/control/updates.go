package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coral-coder/windstream/internal/update"
)

// Updater is implemented by platforms that can install a release.
type Updater interface {
	// UpdateAsset names the release download for this computer.
	UpdateAsset() string
	// InstallUpdate installs the downloaded file and restarts Windstream
	// with it (this process ends shortly after it returns).
	InstallUpdate(file, version string) error
	// UnattendedUpdates reports whether an update may install with nobody
	// at the computer.
	UnattendedUpdates() bool
}

// UpdateFailedNote is a file the platform installer leaves in the update
// folder when an update did not install; its text is shown once.
const UpdateFailedNote = "failed.txt"

const (
	updateFirstCheck = 2 * time.Minute
	updateEvery      = 6 * time.Hour
)

// updateStatus is the dashboard's view of updates.
type updateStatus struct {
	Supported bool   `json:"supported"`
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Page      string `json:"page,omitempty"`
	Available bool   `json:"available"`
	Auto      bool   `json:"auto"`
	// Unattended is false where updates must be installed by hand (macOS).
	Unattended bool      `json:"unattended"`
	State      string    `json:"state,omitempty"` // checking | downloading | installing
	Progress   int       `json:"progress,omitempty"`
	Error      string    `json:"error,omitempty"`
	Checked    time.Time `json:"checked,omitzero"`
}

type updater struct {
	mu      sync.Mutex
	st      updateStatus
	rel     update.Release
	busy    bool
	skip    string // version whose automatic install failed
	checkCh chan struct{}
}

func (u *updater) init() { u.checkCh = make(chan struct{}, 1) }

func (u *updater) set(fn func(*updateStatus)) {
	u.mu.Lock()
	fn(&u.st)
	u.mu.Unlock()
}

func (c *Controller) updatePlatform() (Updater, bool) {
	p, ok := c.opts.Platform.(Updater)
	if !ok || c.opts.Dev || c.opts.Version == "" || c.opts.Version == "dev" || p.UpdateAsset() == "" {
		return nil, false
	}
	return p, true
}

func (c *Controller) updateStatus() updateStatus {
	p, ok := c.updatePlatform()
	c.upd.mu.Lock()
	st := c.upd.st
	c.upd.mu.Unlock()
	st.Supported, st.Current, st.Auto = ok, c.opts.Version, c.Config().Updates.Auto
	if ok {
		st.Unattended = p.UnattendedUpdates()
	}
	return st
}

// checkUpdatesNow asks the update loop for an immediate check.
func (c *Controller) checkUpdatesNow() {
	select {
	case c.upd.checkCh <- struct{}{}:
	default:
	}
}

// runUpdates checks GitHub a couple of minutes after start and every few
// hours, and installs new versions by itself (where that is safe) at a
// moment nobody is streaming.
func (c *Controller) runUpdates(ctx context.Context) {
	p, ok := c.updatePlatform()
	if !ok {
		return
	}
	dir := filepath.Join(c.opts.DataDir, "update")
	if b, err := os.ReadFile(filepath.Join(dir, UpdateFailedNote)); err == nil {
		msg := strings.TrimSpace(string(b))
		c.log.Error("the last update did not install", "detail", msg)
		c.upd.set(func(s *updateStatus) { s.Error = "The last update did not install: " + msg })
		_ = os.Remove(filepath.Join(dir, UpdateFailedNote))
	}
	cleanUpdateDir(dir)

	next := time.Now().Add(updateFirstCheck)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.upd.checkCh:
			next = time.Now()
		case <-tick.C:
		}
		if !time.Now().Before(next) {
			c.checkForUpdate(ctx)
			next = time.Now().Add(updateEvery)
		}
		if c.autoInstallDue(p) {
			if err := c.installUpdate(ctx, p); err != nil {
				c.upd.mu.Lock()
				c.upd.skip = c.upd.rel.Version // not again until the next release
				c.upd.mu.Unlock()
			}
		}
	}
}

func (c *Controller) checkForUpdate(ctx context.Context) {
	c.upd.set(func(s *updateStatus) { s.State = "checking" })
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	rel, err := update.Latest(cctx)
	cancel()
	p, _ := c.updatePlatform()
	c.upd.mu.Lock()
	defer c.upd.mu.Unlock()
	c.upd.st.State = ""
	c.upd.st.Checked = time.Now()
	if err != nil {
		c.log.Debug("update check failed", "error", err)
		c.upd.st.Error = "Could not check for updates: " + err.Error()
		return
	}
	if strings.HasPrefix(c.upd.st.Error, "Could not check") {
		c.upd.st.Error = ""
	}
	available := rel.Newer(c.opts.Version) && p != nil && rel.Has(p.UpdateAsset())
	if available && (!c.upd.st.Available || c.upd.st.Latest != rel.Version) {
		c.log.Info("update available", "version", rel.Version, "running", c.opts.Version)
	}
	c.upd.rel = rel
	c.upd.st.Latest, c.upd.st.Page, c.upd.st.Available = rel.Version, rel.Page, available
}

// autoInstallDue reports whether to install the available update now.
func (c *Controller) autoInstallDue(p Updater) bool {
	if !p.UnattendedUpdates() || !c.Config().Updates.Auto {
		return false
	}
	c.upd.mu.Lock()
	due := c.upd.st.Available && !c.upd.busy && c.upd.skip != c.upd.rel.Version
	c.upd.mu.Unlock()
	return due && c.stackStatus().Stats.Clients == 0 // never in the middle of a game
}

var errUpdateBusy = errors.New("an update is already being installed")

// installUpdate downloads the available release, checks it and hands it to
// the platform installer.
func (c *Controller) installUpdate(ctx context.Context, p Updater) error {
	c.upd.mu.Lock()
	if c.upd.busy {
		c.upd.mu.Unlock()
		return errUpdateBusy
	}
	if !c.upd.st.Available {
		c.upd.mu.Unlock()
		return errors.New("no update available")
	}
	c.upd.busy = true
	rel := c.upd.rel
	c.upd.st.State, c.upd.st.Progress, c.upd.st.Error = "downloading", 0, ""
	c.upd.mu.Unlock()
	defer func() {
		c.upd.mu.Lock()
		c.upd.busy = false
		c.upd.mu.Unlock()
	}()
	fail := func(err error) error {
		c.log.Error("update failed", "version", rel.Version, "error", err)
		c.upd.set(func(s *updateStatus) { s.State, s.Error = "", "Update to "+rel.Version+" failed: "+err.Error() })
		return err
	}
	c.log.Info("downloading update", "version", rel.Version)
	dir := filepath.Join(c.opts.DataDir, "update")
	file, err := update.Fetch(ctx, rel, p.UpdateAsset(), dir, func(done, total int64) {
		if total > 0 {
			c.upd.set(func(s *updateStatus) { s.Progress = int(done * 100 / total) })
		}
	})
	if err != nil {
		return fail(err)
	}
	c.upd.set(func(s *updateStatus) { s.State = "installing" })
	c.log.Info("installing update; Windstream restarts in a moment", "version", rel.Version)
	if err := p.InstallUpdate(file, rel.Version); err != nil {
		return fail(err)
	}
	return nil
}

// cleanUpdateDir removes downloads from earlier updates (best effort: the
// installer of the current one may still be finishing).
func cleanUpdateDir(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != UpdateFailedNote {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

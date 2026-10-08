// Package control is the brain of the desktop app: it owns the
// configuration, installs dependencies, keeps the router and certificate
// current, runs the streaming engine and serves the local dashboard.
package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coral-coder/windstream/internal/auth"
	"github.com/coral-coder/windstream/internal/config"
	"github.com/coral-coder/windstream/internal/deps"
	"github.com/coral-coder/windstream/internal/display"
	"github.com/coral-coder/windstream/internal/input"
	"github.com/coral-coder/windstream/internal/media"
	"github.com/coral-coder/windstream/internal/netx"
	"github.com/coral-coder/windstream/internal/server"
	"github.com/coral-coder/windstream/internal/stream"

	"github.com/coral-coder/windstream/internal/safe"
)

// Platform supplies OS integration (Windows implementation lives in winapp).
type Platform interface {
	// SteamExe returns the Steam executable, or "".
	SteamExe() string
	// Uninstall removes Windstream from the PC (runs after the response).
	Uninstall(removeData bool) error
	// Quit stops the app.
	Quit()
	// PanelReady publishes the dashboard address (it can move off the
	// default port if something else uses it).
	PanelReady(url string)
}

// Options configures a controller.
type Options struct {
	DataDir string
	// PanelAddr forces the dashboard address; empty = 127.0.0.1:<panel_port>,
	// moving to a free port if that one is taken.
	PanelAddr string
	Version   string
	Log       *slog.Logger
	Logs      *LogBuffer
	Platform  Platform
	// Dev runs with the synthetic source and no router/driver changes, for
	// development on any OS.
	Dev bool
	// LastCrash summarizes why the previous server process died, when the
	// supervisor restarted it; the dashboard shows it.
	LastCrash string
}

// Controller runs everything.
type Controller struct {
	opts    Options
	log     *slog.Logger
	cfgPath string

	mu  sync.Mutex
	cfg *config.Config
	// configNotice explains a settings file that had to be repaired at
	// startup; the dashboard shows it.
	configNotice string

	auth     *auth.Authenticator
	sessions *auth.SessionStore
	limiter  *auth.Limiter
	deps     *deps.Manager
	net      *netx.Manager
	tls      *server.AutoTLS
	srv      *server.Server

	stackMu    sync.Mutex
	disp       *display.Manager
	hub        *stream.Hub
	stackErr   string
	stackMode  string
	restartReq chan struct{}

	panel     *panel
	panelAddr string

	httpsMu     sync.Mutex // serializes HTTPS server restarts
	httpsCancel context.CancelFunc
	httpsDone   chan struct{}
	httpsErr    atomic.Pointer[string] // separate: the server goroutine sets it

	// identity is a random token the HTTPS server publishes, so the app can
	// check that its public link reaches this PC and not something else.
	identity string

	netMu     sync.Mutex
	netCancel context.CancelFunc
	netDone   chan struct{}
	runCtx    context.Context
}

// New loads (or creates) the configuration in DataDir.
func New(opts Options) (*Controller, error) {
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, err
	}
	c := &Controller{opts: opts, log: opts.Log, cfgPath: filepath.Join(opts.DataDir, "windstream.toml"),
		restartReq: make(chan struct{}, 1)}
	var tok [16]byte
	if _, err := rand.Read(tok[:]); err != nil {
		return nil, err
	}
	c.identity = hex.EncodeToString(tok[:])
	def := config.AppDefaults()
	if opts.Dev {
		def.Display.Mode = "test"
		def.Audio.Backend = "test"
		def.Network.UPnP = false
		def.Video.Encoder = "x264"
		def.Video.FPS = 30
		def.Video.BitrateKbps = 4000
		def.Display.Width, def.Display.Height = 1280, 720
	}
	cfg, err := config.Load(c.cfgPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		cfg = &def
		if err := cfg.Save(c.cfgPath); err != nil {
			return nil, err
		}
	case err != nil:
		// Never refuse to start over the settings file: keep a copy of it,
		// repair what is broken (accounts and valid sections survive) and
		// say so on the dashboard.
		data, rerr := os.ReadFile(c.cfgPath)
		if rerr != nil {
			return nil, fmt.Errorf("config %s: %w", c.cfgPath, rerr)
		}
		backup := c.cfgPath + ".broken-" + time.Now().Format("20060102-150405")
		_ = os.WriteFile(backup, data, 0o600)
		repaired, what := config.Repair(data, def)
		cfg = &repaired
		if serr := cfg.Save(c.cfgPath); serr != nil {
			return nil, serr
		}
		c.configNotice = fmt.Sprintf("The settings file had a problem (%s). %s The original was saved as %s.",
			trimErr(err), what, filepath.Base(backup))
		opts.Log.Error("settings file repaired", "error", err, "changes", what, "backup", backup)
	}
	if len(cfg.UnknownKeys) > 0 {
		opts.Log.Warn("ignoring settings this version does not know (from a newer version?)", "keys", cfg.UnknownKeys)
	}
	c.cfg = cfg
	c.auth = auth.NewAuthenticator(nil, true)
	c.auth.SetUsers(server.UsersFromConfig(cfg), cfg.Auth.RequireTOTP)
	c.sessions = auth.NewSessionStore(cfg.Auth.SessionIdleTimeout.Duration, cfg.Auth.SessionMaxAge.Duration)
	c.limiter = auth.NewLimiter(cfg.Auth.LoginRateLimit, cfg.Auth.LoginRateWindow.Duration)
	c.deps = deps.NewManager(opts.DataDir, opts.Log)
	tlsMgr, err := server.NewAutoTLS(filepath.Join(opts.DataDir, "tls"), cfg.Network.ACMEEmail, opts.Log)
	if err != nil {
		return nil, err
	}
	c.tls = tlsMgr
	return c, nil
}

// Config returns a copy of the current configuration.
func (c *Controller) Config() config.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.cfg
}

// update mutates, validates and saves the config.
func (c *Controller) update(fn func(*config.Config) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := *c.cfg
	next.Users = append([]config.User(nil), c.cfg.Users...)
	if err := fn(&next); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := next.Save(c.cfgPath); err != nil {
		return err
	}
	c.cfg = &next
	c.auth.SetUsers(server.UsersFromConfig(&next), next.Auth.RequireTOTP)
	return nil
}

func (c *Controller) ports() netx.Ports {
	cfg := c.Config()
	_, port, _ := net.SplitHostPort(cfg.Server.Listen)
	var p uint16
	fmt.Sscan(port, &p)
	return netx.Ports{HTTPSInternal: p, HTTPSExternal: uint16(cfg.Network.HTTPSExternalPort),
		MediaUDP: uint16(cfg.WebRTC.UDPPort), MediaTCP: uint16(cfg.WebRTC.TCPPort)}
}

// PanelURL is the dashboard address.
func (c *Controller) PanelURL() string { return "http://" + c.panelAddr + "/" }

// ensurePorts moves any port another program already uses to a free one and
// saves the choice, so the app starts no matter what else runs on the PC.
func (c *Controller) ensurePorts() error {
	cfg := c.Config()
	https, media, iceTCP, panelPort := cfg.ListenPort(), cfg.WebRTC.UDPPort, cfg.WebRTC.TCPPort, cfg.Server.PanelPort
	changed := false
	if https == 0 || !netx.TCPFree(https) {
		p, err := netx.PickPort(netx.TCPFree, []int{8443, 9443, 10443, 18443, 28443}, panelPort, iceTCP)
		if err != nil {
			return fmt.Errorf("no free port for HTTPS: %w", err)
		}
		c.log.Warn("HTTPS port is in use by another program; switching", "busy", https, "now", p)
		https, changed = p, true
	}
	if !netx.UDPFree(media) {
		p, err := netx.PickPort(netx.UDPFree, []int{8444, 9444, 10444, 18444, 28444})
		if err != nil {
			return fmt.Errorf("no free UDP port for media: %w", err)
		}
		c.log.Warn("media UDP port is in use by another program; switching", "busy", media, "now", p)
		media, changed = p, true
	}
	if iceTCP != 0 && !netx.TCPFree(iceTCP) {
		p, err := netx.PickPort(netx.TCPFree, []int{media, 9445, 10445}, https, panelPort)
		if err != nil {
			return fmt.Errorf("no free TCP port for media fallback: %w", err)
		}
		iceTCP, changed = p, true
	}
	if c.opts.PanelAddr != "" {
		c.panelAddr = c.opts.PanelAddr
	} else {
		if !netx.LoopbackTCPFree(panelPort) {
			p, err := netx.PickPort(netx.LoopbackTCPFree, []int{47333, 47334, 47335, 47336, 47337, 47338}, https, iceTCP)
			if err != nil {
				return fmt.Errorf("no free port for the dashboard: %w", err)
			}
			c.log.Warn("dashboard port is in use by another program; switching", "busy", panelPort, "now", p)
			panelPort, changed = p, true
		}
		c.panelAddr = fmt.Sprintf("127.0.0.1:%d", panelPort)
	}
	if !changed {
		return nil
	}
	return c.update(func(cf *config.Config) error {
		cf.Server.Listen = fmt.Sprintf(":%d", https)
		cf.WebRTC.UDPPort, cf.WebRTC.TCPPort, cf.Server.PanelPort = media, iceTCP, panelPort
		return nil
	})
}

// restartHTTPS (re)starts the public HTTPS server on the configured port.
// Failures are shown on the dashboard instead of stopping the app.
func (c *Controller) restartHTTPS() {
	c.httpsMu.Lock()
	defer c.httpsMu.Unlock()
	if c.httpsCancel != nil {
		c.httpsCancel()
		<-c.httpsDone
	}
	cfg := c.Config()
	srv, err := server.New(&cfg, nil, c.log, server.Options{
		Auth: c.auth, Sessions: c.sessions, Limiter: c.limiter, GetCertificate: c.tls.GetCertificate,
		Identity: c.identity,
	})
	if err != nil {
		c.setHTTPSErr(err.Error())
		return
	}
	c.stackMu.Lock()
	srv.SetHub(c.hub)
	c.srv = srv
	c.stackMu.Unlock()
	ctx, cancel := context.WithCancel(c.runCtx)
	done := make(chan struct{})
	c.httpsCancel, c.httpsDone = cancel, done
	c.setHTTPSErr("")
	go func() {
		defer close(done)
		defer safe.Recover(c.log, "https server")
		if err := srv.Run(ctx); err != nil && ctx.Err() == nil {
			c.log.Error("HTTPS server stopped", "error", err)
			c.setHTTPSErr(err.Error())
		}
	}()
}

func (c *Controller) setHTTPSErr(s string) { c.httpsErr.Store(&s) }

func (c *Controller) httpsError() string {
	if p := c.httpsErr.Load(); p != nil {
		return *p
	}
	return ""
}

// Run blocks until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	c.runCtx = ctx
	if err := c.ensurePorts(); err != nil {
		return err
	}
	c.panel = newPanel(c)
	ln, err := c.panel.listen()
	if err != nil && c.opts.PanelAddr == "" {
		// Something grabbed the port since it was checked: move once more.
		c.log.Warn("dashboard port was taken at the last moment; moving", "error", err)
		cfg := c.Config()
		if p, perr := netx.PickPort(netx.LoopbackTCPFree, []int{47334, 47335, 47336, 47337, 47338, 47339}, cfg.ListenPort(), cfg.WebRTC.TCPPort); perr == nil {
			c.panelAddr = fmt.Sprintf("127.0.0.1:%d", p)
			_ = c.update(func(cf *config.Config) error { cf.Server.PanelPort = p; return nil })
			ln, err = c.panel.listen()
		}
	}
	if err != nil {
		return err
	}
	panelErr := make(chan error, 1)
	go func() {
		defer safe.Recover(c.log, "dashboard server")
		panelErr <- c.panel.serve(ctx, ln)
	}()
	// Published only now that the dashboard is listening, so the launcher
	// and tray never open an address nothing (or something else) answers.
	if c.opts.Platform != nil {
		c.opts.Platform.PanelReady(c.PanelURL())
	}

	c.restartHTTPS()
	c.restartNetwork()

	safego(c.log, "dependency setup", func() {
		if !c.opts.Dev {
			c.writeVDDMode()
		}
		c.deps.EnsureAll(ctx)
		c.requestRestart()
	})

	c.log.Info("windstream running", "dashboard", c.PanelURL(), "https_port", c.Config().ListenPort(),
		"media_udp_port", c.Config().WebRTC.UDPPort, "version", c.opts.Version)
	for {
		select {
		case <-ctx.Done():
			c.stopStack()
			c.httpsMu.Lock()
			if c.httpsDone != nil {
				<-c.httpsDone
			}
			c.httpsMu.Unlock()
			return nil
		case err := <-panelErr:
			if err != nil {
				c.stopStack()
				return fmt.Errorf("dashboard: %w", err)
			}
		case <-c.restartReq:
			// Coalesce bursts (settings save + network change).
			time.Sleep(300 * time.Millisecond)
			select {
			case <-c.restartReq:
			default:
			}
			c.safeRestartStack(ctx)
		}
	}
}

// restartNetwork (re)creates the router/public-address manager from the
// current settings; the old one removes its port mappings on the way out.
func (c *Controller) restartNetwork() {
	c.netMu.Lock()
	defer c.netMu.Unlock()
	if c.netCancel != nil {
		c.netCancel()
		// Let the old manager remove its router mappings before the new one
		// adds its own (they may be the same ports).
		select {
		case <-c.netDone:
		case <-time.After(15 * time.Second):
		}
	}
	cfg := c.Config()
	ctx, cancel := context.WithCancel(c.runCtx)
	done := make(chan struct{})
	c.netCancel, c.netDone = cancel, done
	var last netx.Status
	n := netx.NewManager(c.log, c.ports(), cfg.Network.UPnP && !c.opts.Dev, cfg.Network.CustomDomain, func(st netx.Status) {
		c.tls.SetHost(st.Hostname)
		// Let's Encrypt validates on port 443 only; anywhere else it can
		// only fail (and repeated failures get the address rate limited).
		// A failed self-check with the router mapped is most likely the
		// router not looping back to itself, which does not stop the link
		// (or Let's Encrypt) from working from outside.
		c.tls.SetACME(st.HTTPSExternal == 443 && !st.CGNAT && (st.PublicCheck == "ok" || st.UPnP == "mapped"))
		c.tls.Warm(ctx)
		// The streaming engine only cares about the public IP.
		if st.PublicIP != last.PublicIP || st.CGNAT != last.CGNAT {
			c.requestRestart()
		}
		last = st
	})
	n.SetVerifier(func(ctx context.Context, url string) error {
		return netx.CheckIdentity(ctx, url, c.identity)
	})
	c.net = n
	safego(c.log, "network", func() {
		defer close(done)
		n.Run(ctx)
	})
}

func (c *Controller) netStatus() netx.Status {
	c.netMu.Lock()
	n := c.net
	c.netMu.Unlock()
	if n == nil {
		return netx.Status{}
	}
	return n.Status()
}

func (c *Controller) requestRestart() {
	select {
	case c.restartReq <- struct{}{}:
	default:
	}
}

func (c *Controller) writeVDDMode() {
	cfg := c.Config()
	c.deps.VDDWidth, c.deps.VDDHeight, c.deps.VDDRefresh = cfg.Display.Width, cfg.Display.Height, cfg.Display.Refresh
}

func (c *Controller) stopStack() {
	c.stackMu.Lock()
	defer c.stackMu.Unlock()
	if c.srv != nil {
		c.srv.SetHub(nil)
	}
	if c.hub != nil {
		c.hub.Close()
		c.hub = nil
	}
	c.disp = nil // the hub released it when its pipelines stopped
}

// displayLease turns the display on when the first viewer starts a capture
// session and restores the desktop when the last one leaves, so the PC's own
// screen layout is untouched while nobody is streaming.
type displayLease struct {
	mu   sync.Mutex
	n    int
	disp *display.Manager
	ctx  context.Context
}

func (l *displayLease) acquire(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n == 0 {
		if err := l.disp.Start(ctx); err != nil {
			l.disp.Stop()
			return err
		}
	}
	l.n++
	return nil
}

func (l *displayLease) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n == 0 {
		return
	}
	l.n--
	if l.n == 0 {
		l.disp.Stop()
	}
}

// restartStack (re)builds the display + capture/encode + WebRTC engine from
// the current settings.
func (c *Controller) restartStack(ctx context.Context) {
	c.stopStack()
	c.stackMu.Lock()
	defer c.stackMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	cfg := c.Config()
	if !c.deps.Ready("ffmpeg") {
		c.stackErr = "Waiting for the encoder (ffmpeg) to be installed"
		return
	}

	// Pick the display mode that can actually work right now.
	mode := cfg.Display.Mode
	if mode == "virtual" && !c.deps.Ready("vdd") {
		c.log.Warn("virtual display driver not ready; streaming the primary monitor instead")
		mode = "monitor"
		cfg.Display.Monitor = "0"
		for i, o := range listOutputs() {
			if o.Primary {
				cfg.Display.Monitor = fmt.Sprint(i)
			}
		}
	}
	cfg.Display.Mode = mode
	if mode == "virtual" {
		if changed, err := deps.WriteVDDSettings(deps.VDDConfigDir, cfg.Display.Width, cfg.Display.Height, cfg.Display.Refresh); err == nil && changed {
			c.log.Info("virtual display modes updated")
		}
	}
	if _, err := os.Stat(filepath.Join(c.opts.DataDir, "vdd.installed")); err == nil {
		cfg.Display.VirtualOwned = true
	}
	disp := display.New(cfg.Display, c.log)
	// Nothing streams yet: undo anything a crashed previous run left behind
	// (virtual monitor still on, primary display moved).
	disp.RecoverUnclean()
	lease := &displayLease{disp: disp, ctx: ctx}
	webrtcCfg := cfg.WebRTC
	if st := c.netStatus(); st.PublicIP != "" && !st.CGNAT {
		webrtcCfg.PublicIPs = []string{st.PublicIP}
	}
	hcfg := HubConfig(&cfg, disp, c.deps.FFmpegPath(), webrtcCfg)
	hcfg.Acquire, hcfg.Release = lease.acquire, lease.release
	hub, err := stream.NewHub(ctx, hcfg, c.log)
	if err != nil {
		c.stackErr = "Streaming engine: " + err.Error()
		c.log.Error("hub start failed", "error", err)
		return
	}
	c.disp, c.hub, c.stackErr, c.stackMode = disp, hub, "", mode
	c.srv.SetHub(hub)
	c.log.Info("streaming engine ready", "display", mode)
}

// safeRestartStack restarts the engine, turning a panic into a dashboard
// error instead of a crash.
func (c *Controller) safeRestartStack(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("engine restart crashed (recovered)", "panic", r, "stack", string(debug.Stack()))
			c.stackMu.Lock()
			c.stackErr = fmt.Sprintf("internal error starting the stream engine: %v", r)
			c.stackMu.Unlock()
		}
	}()
	c.restartStack(ctx)
}

// HubConfig builds the streaming engine configuration.
func HubConfig(cfg *config.Config, disp *display.Manager, ffmpeg string, webrtcCfg config.WebRTC) stream.Config {
	return stream.Config{
		Video: media.VideoConfig{
			FFmpeg: ffmpeg, Width: cfg.Display.Width, Height: cfg.Display.Height,
			FPS: cfg.Video.FPS, BitrateKbps: cfg.Video.BitrateKbps, GOPFrames: cfg.Video.GOPFrames(),
			Encoder: cfg.Video.Encoder, Preset: cfg.Video.Preset, Profile: cfg.Video.Profile,
			ShowCursor: cfg.Video.ShowCursor, ExtraArgs: cfg.Video.ExtraArgs,
		},
		Source: func() (media.Source, error) {
			t, err := disp.Target()
			if err != nil {
				return media.Source{}, err
			}
			if t.Test {
				return media.Source{Test: true, Adapter: cfg.Video.GPU}, nil
			}
			return media.Source{Adapter: t.Adapter, Output: t.Output}, nil
		},
		Audio: media.AudioConfig{FFmpeg: ffmpeg, Backend: cfg.Audio.Backend,
			Device: cfg.Audio.Device, BitrateKbps: cfg.Audio.BitrateKbps},
		Codec:        cfg.Video.Codec,
		AudioEnabled: cfg.Audio.Enabled,
		WebRTC:       webrtcCfg,
		Input: input.Options{Gamepads: cfg.Input.Gamepads, Keyboard: cfg.Input.Keyboard,
			Mouse: cfg.Input.Mouse, MaxGamepads: cfg.Input.MaxGamepads},
		InputEnabled: cfg.Input.Enabled,
		MaxClients:   cfg.Server.MaxClients,
	}
}

func listOutputs() []display.Output {
	outs, _ := display.List()
	return outs
}

// stackStatus is shown on the dashboard.
type stackStatus struct {
	Running bool         `json:"running"`
	Mode    string       `json:"mode"`
	Error   string       `json:"error,omitempty"`
	Stats   stream.Stats `json:"stats"`
}

func (c *Controller) stackStatus() stackStatus {
	c.stackMu.Lock()
	defer c.stackMu.Unlock()
	st := stackStatus{Running: c.hub != nil, Mode: c.stackMode, Error: c.stackErr}
	if c.hub != nil {
		st.Stats = c.hub.Stats()
	}
	return st
}

func trimErr(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	return s
}

// Link is the address people play from (public if available, else LAN).
func (c *Controller) Link() string { return c.netStatus().Link() }

// TrayStatus is a one-line status for the tray tooltip.
func (c *Controller) TrayStatus() string {
	if len(c.Config().Users) == 0 {
		return "Windstream – open to finish setup"
	}
	st := c.stackStatus()
	switch {
	case st.Running && st.Stats.Clients > 0:
		return fmt.Sprintf("Windstream – streaming to %d", st.Stats.Clients)
	case st.Running:
		return "Windstream – ready"
	default:
		return "Windstream – setting up"
	}
}

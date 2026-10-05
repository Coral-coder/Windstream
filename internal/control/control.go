// Package control is the brain of the desktop app: it owns the
// configuration, installs dependencies, keeps the router and certificate
// current, runs the streaming engine and serves the local dashboard.
package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
)

// Platform supplies OS integration (Windows implementation lives in winapp).
type Platform interface {
	// SteamExe returns the Steam executable, or "".
	SteamExe() string
	// Uninstall removes Windstream from the PC (runs after the response).
	Uninstall(removeData bool) error
	// Quit stops the app.
	Quit()
}

// Options configures a controller.
type Options struct {
	DataDir   string
	PanelAddr string // loopback address of the dashboard, e.g. 127.0.0.1:47333
	Version   string
	Log       *slog.Logger
	Logs      *LogBuffer
	Platform  Platform
	// Dev runs with the synthetic source and no router/driver changes, for
	// development on any OS.
	Dev bool
}

// Controller runs everything.
type Controller struct {
	opts    Options
	log     *slog.Logger
	cfgPath string

	mu  sync.Mutex
	cfg *config.Config

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

	panel *panel

	netMu     sync.Mutex
	netCancel context.CancelFunc
	runCtx    context.Context
}

// New loads (or creates) the configuration in DataDir.
func New(opts Options) (*Controller, error) {
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, err
	}
	c := &Controller{opts: opts, log: opts.Log, cfgPath: filepath.Join(opts.DataDir, "windstream.toml"),
		restartReq: make(chan struct{}, 1)}
	cfg, err := config.Load(c.cfgPath)
	if errors.Is(err, os.ErrNotExist) {
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
		cfg = &def
		if err := cfg.Save(c.cfgPath); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("config %s: %w", c.cfgPath, err)
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
func (c *Controller) PanelURL() string { return "http://" + c.opts.PanelAddr + "/" }

// Run blocks until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	cfg := c.Config()
	var err error
	c.srv, err = server.New(&cfg, nil, c.log, server.Options{
		Auth: c.auth, Sessions: c.sessions, Limiter: c.limiter, GetCertificate: c.tls.GetCertificate,
	})
	if err != nil {
		return err
	}
	c.panel = newPanel(c)
	panelErr := make(chan error, 1)
	go func() { panelErr <- c.panel.run(ctx) }()

	srvErr := make(chan error, 1)
	go func() { srvErr <- c.srv.Run(ctx) }()

	c.runCtx = ctx
	c.restartNetwork()

	go func() {
		if c.opts.Dev {
			c.deps.EnsureAll(ctx) // only probes for a system ffmpeg
		} else {
			c.writeVDDMode()
			c.deps.EnsureAll(ctx)
		}
		c.requestRestart()
	}()

	c.log.Info("windstream running", "dashboard", c.PanelURL(), "version", c.opts.Version)
	for {
		select {
		case <-ctx.Done():
			c.stopStack()
			<-srvErr
			return nil
		case err := <-srvErr:
			c.stopStack()
			return fmt.Errorf("https server: %w", err)
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
			c.restartStack(ctx)
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
	}
	cfg := c.Config()
	ctx, cancel := context.WithCancel(c.runCtx)
	c.netCancel = cancel
	c.net = netx.NewManager(c.log, c.ports(), cfg.Network.UPnP && !c.opts.Dev, cfg.Network.CustomDomain, func(st netx.Status) {
		c.tls.SetHost(st.Hostname)
		c.tls.Warm(ctx)
		c.requestRestart()
	})
	go c.net.Run(ctx)
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
func (c *Controller) Link() string {
	st := c.netStatus()
	if st.PublicURL != "" {
		return st.PublicURL
	}
	return st.LANURL
}

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

// Package config loads and validates the Windstream server configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration is a time.Duration that decodes from TOML strings such as "12h".
type Duration struct{ time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.Duration.String()), nil }

// Config is the root configuration document.
type Config struct {
	Server  Server  `toml:"server"`
	TLS     TLS     `toml:"tls"`
	Auth    Auth    `toml:"auth"`
	Users   []User  `toml:"users"`
	Display Display `toml:"display"`
	Video   Video   `toml:"video"`
	Audio   Audio   `toml:"audio"`
	WebRTC  WebRTC  `toml:"webrtc"`
	Input   Input   `toml:"input"`
	Log     Log     `toml:"log"`
	Network Network `toml:"network"`
}

// Network configures automatic internet reachability (desktop app).
type Network struct {
	// UPnP asks the router to forward the ports automatically.
	UPnP bool `toml:"upnp"`
	// HTTPSExternalPort is the public HTTPS port. 443 lets Let's Encrypt
	// validate with TLS-ALPN-01 and gives links without a port number.
	HTTPSExternalPort int `toml:"https_external_port"`
	// CustomDomain replaces the automatic <ip>.sslip.io name.
	CustomDomain string `toml:"custom_domain"`
	// ACMEEmail is optional (Let's Encrypt expiry notices).
	ACMEEmail string `toml:"acme_email"`
}

// Server holds HTTP(S) listener settings.
type Server struct {
	// Listen is the HTTPS listen address, e.g. ":8443".
	Listen string `toml:"listen"`
	// RedirectHTTP, when set (e.g. ":80"), serves a plain HTTP listener that
	// redirects to HTTPS. Required for ACME HTTP-01 challenges.
	RedirectHTTP string `toml:"redirect_http"`
	// AllowedOrigins lists extra origins (scheme://host[:port]) accepted for
	// state-changing requests and WebSocket upgrades. The request's own
	// https://Host origin is always accepted.
	AllowedOrigins []string `toml:"allowed_origins"`
	// BehindProxy trusts X-Forwarded-For for client IP (rate limiting/logging).
	// Only enable when a trusted reverse proxy sits in front of Windstream.
	BehindProxy bool `toml:"behind_proxy"`
	// MaxClients caps concurrent streaming sessions.
	MaxClients int `toml:"max_clients"`
	// PanelPort is the loopback port of the desktop app's dashboard.
	PanelPort int `toml:"panel_port"`
}

// TLS selects the certificate source. Exactly one of cert files, ACME, or
// self-signed must be configured.
type TLS struct {
	CertFile   string `toml:"cert_file"`
	KeyFile    string `toml:"key_file"`
	SelfSigned bool   `toml:"self_signed"`
	// Auto: certificates are managed by the desktop app (Let's Encrypt for
	// the public name, persistent self-signed for LAN).
	Auto         bool     `toml:"auto"`
	ACME         bool     `toml:"acme"`
	ACMEDomains  []string `toml:"acme_domains"`
	ACMEEmail    string   `toml:"acme_email"`
	ACMECacheDir string   `toml:"acme_cache_dir"`
}

// Auth holds session and login hardening settings.
type Auth struct {
	SessionIdleTimeout Duration `toml:"session_idle_timeout"`
	SessionMaxAge      Duration `toml:"session_max_age"`
	LoginRateLimit     int      `toml:"login_rate_limit"`
	LoginRateWindow    Duration `toml:"login_rate_window"`
	// RequireTOTP refuses login for users without a TOTP secret configured.
	RequireTOTP bool `toml:"require_totp"`
}

// User is a login account. PasswordHash is an argon2id PHC string produced by
// `windstream hash-password`. TOTPSecret is an optional base32 secret from
// `windstream totp-setup`.
type User struct {
	Name         string `toml:"name"`
	PasswordHash string `toml:"password_hash"`
	TOTPSecret   string `toml:"totp_secret"`
}

// Display describes which screen is streamed and how it is created.
type Display struct {
	// Mode is one of:
	//   "virtual" - turn on an Indirect Display Driver monitor (e.g. the
	//               open-source Virtual Display Driver) while streaming, set
	//               its resolution, and capture it. Needs no physical monitor.
	//   "monitor" - capture an existing output (physical monitor or HDMI
	//               dummy plug), chosen by Monitor.
	//   "test"    - synthetic test pattern; for checking the network path
	//               and client without touching the desktop.
	Mode    string `toml:"mode"`
	Width   int    `toml:"width"`
	Height  int    `toml:"height"`
	Refresh int    `toml:"refresh"`

	// Monitor selects the output in "monitor" mode. It is either an index as
	// printed by `windstream displays` (e.g. "0") or a GDI device name
	// (e.g. "\\.\DISPLAY2").
	Monitor string `toml:"monitor"`
	// SetMode applies Width x Height @ Refresh to the captured monitor in
	// "monitor" mode (always applied in "virtual" mode).
	SetMode bool `toml:"set_mode"`

	// VirtualDevice is a case-insensitive substring of the virtual display
	// adapter's Device Manager name.
	VirtualDevice string `toml:"virtual_device"`
	// VirtualKeepEnabled leaves the virtual monitor on when Windstream exits.
	VirtualKeepEnabled bool `toml:"virtual_keep_enabled"`
	// VirtualOwned (runtime only) means Windstream installed the virtual
	// display adapter, so it may switch it off even if it found it enabled.
	VirtualOwned bool `toml:"-"`
	// MakePrimary makes the streamed monitor the primary display while
	// Windstream runs, so games and Big Picture open on it. The previous
	// primary is restored at exit.
	MakePrimary bool `toml:"make_primary"`

	// Launch is started once the display is ready, e.g.
	// ["C:\\Program Files (x86)\\Steam\\steam.exe", "steam://open/bigpicture"].
	Launch         []string `toml:"launch"`
	StartupTimeout Duration `toml:"startup_timeout"`
}

// Video configures capture and encoding.
type Video struct {
	// Encoder is one of auto, nvenc, amf, qsv, x264. "auto" probes the GPU
	// encoders in that order with a one-frame test encode.
	Encoder string `toml:"encoder"`
	// Codec is auto, av1, h265 or h264. "auto" streams the most efficient
	// codec that both this PC's encoder and the viewer's browser handle in
	// hardware (AV1, then HEVC, then H.264).
	Codec       string  `toml:"codec"`
	FPS         int     `toml:"fps"`
	BitrateKbps int     `toml:"bitrate_kbps"`
	GOPSeconds  float64 `toml:"gop_seconds"`
	Preset      string  `toml:"preset"`
	// Profile is the H.264 profile: high (best quality per bit), main, or
	// baseline (only needed for unusual decoders).
	Profile string `toml:"profile"`
	// GPU is the DXGI adapter index used for encoding in monitor/test mode.
	// In virtual mode the adapter the virtual monitor renders on is used.
	GPU          int      `toml:"gpu"`
	FFmpegBinary string   `toml:"ffmpeg_binary"`
	ExtraArgs    []string `toml:"extra_args"`
	ShowCursor   bool     `toml:"show_cursor"`
}

// Audio configures audio capture.
type Audio struct {
	Enabled bool `toml:"enabled"`
	// Backend is "wasapi" (loopback of a playback device: what you hear),
	// "dshow" (a DirectShow capture device, e.g. a virtual cable) or "test"
	// (a sine tone).
	Backend string `toml:"backend"`
	// Device is the playback device name substring for wasapi (empty = the
	// default output) or the DirectShow device name for dshow.
	Device      string `toml:"device"`
	BitrateKbps int    `toml:"bitrate_kbps"`
}

// WebRTC configures ICE transport.
type WebRTC struct {
	// UDPPort is the single UDP port all media is multiplexed on. Forward it.
	UDPPort int `toml:"udp_port"`
	// TCPPort optionally enables ICE-TCP as a fallback for restrictive networks.
	TCPPort int `toml:"tcp_port"`
	// PublicIPs are advertised as host candidates (1:1 NAT). Set this to your
	// public IP or DDNS-resolved address to avoid STUN on the server side.
	PublicIPs []string `toml:"public_ips"`
	// STUNServers are handed to the browser client.
	STUNServers     []string `toml:"stun_servers"`
	IncludeLoopback bool     `toml:"include_loopback"`
}

// Input toggles which input classes clients may drive.
type Input struct {
	Enabled     bool `toml:"enabled"`
	Gamepads    bool `toml:"gamepads"`
	Keyboard    bool `toml:"keyboard"`
	Mouse       bool `toml:"mouse"`
	MaxGamepads int  `toml:"max_gamepads"`
}

// Log configures logging.
type Log struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
	// File appends logs to this path instead of stderr (needed when running
	// windstreamw.exe, which has no console).
	File string `toml:"file"`
}

// Default returns the baseline configuration.
func Default() Config {
	return Config{
		Server: Server{Listen: ":8443", MaxClients: 2, PanelPort: 47333},
		TLS:    TLS{ACMECacheDir: "acme-cache"},
		Auth: Auth{
			SessionIdleTimeout: Duration{12 * time.Hour},
			SessionMaxAge:      Duration{7 * 24 * time.Hour},
			LoginRateLimit:     5,
			LoginRateWindow:    Duration{time.Minute},
		},
		Display: Display{
			Mode: "virtual", Width: 1920, Height: 1080, Refresh: 60, Monitor: "0",
			VirtualDevice: "Virtual Display", MakePrimary: true, StartupTimeout: Duration{15 * time.Second},
		},
		Video: Video{
			Encoder: "auto", Codec: "auto", FPS: 60, BitrateKbps: 15000, GOPSeconds: 2,
			FFmpegBinary: "ffmpeg", ShowCursor: true, Profile: "high",
		},
		Audio:   Audio{Enabled: true, Backend: "wasapi", BitrateKbps: 128},
		WebRTC:  WebRTC{UDPPort: 8444, STUNServers: []string{"stun:stun.l.google.com:19302"}},
		Input:   Input{Enabled: true, Gamepads: true, Keyboard: true, Mouse: true, MaxGamepads: 4},
		Log:     Log{Level: "info", Format: "text"},
		Network: Network{UPnP: true, HTTPSExternalPort: 443},
	}
}

// AppDefaults is the configuration the desktop app starts with.
func AppDefaults() Config {
	c := Default()
	c.TLS = TLS{Auto: true}
	c.Auth.RequireTOTP = true
	return c
}

// Save writes the configuration atomically with owner-only permissions.
func (c *Config) Save(path string) error {
	var b strings.Builder
	b.WriteString("# Managed by Windstream. Use the dashboard to change settings.\n\n")
	if err := toml.NewEncoder(&b).Encode(c); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads a TOML file over the defaults and validates the result.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes TOML bytes over the defaults and validates the result.
// Unknown keys are rejected so typos never silently disable a setting.
func Parse(data []byte) (*Config, error) {
	cfg := Default()
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return nil, fmt.Errorf("unknown config keys: %s", strings.Join(keys, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

var validEncoders = map[string]bool{"auto": true, "nvenc": true, "amf": true, "qsv": true, "x264": true}

// Validate checks the configuration for consistency.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if strings.TrimSpace(c.Server.Listen) == "" {
		add("server.listen must be set")
	}
	if c.Server.PanelPort < 1 || c.Server.PanelPort > 65535 {
		add("server.panel_port must be 1..65535")
	}
	if c.Server.MaxClients < 1 {
		add("server.max_clients must be >= 1")
	}
	for _, o := range c.Server.AllowedOrigins {
		u, err := url.Parse(o)
		if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" {
			add("server.allowed_origins entry %q must be scheme://host[:port]", o)
		}
	}

	sources := 0
	if c.TLS.CertFile != "" || c.TLS.KeyFile != "" {
		sources++
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			add("tls.cert_file and tls.key_file must both be set")
		}
	}
	if c.TLS.ACME {
		sources++
		if len(c.TLS.ACMEDomains) == 0 {
			add("tls.acme requires tls.acme_domains")
		}
		if c.Server.RedirectHTTP == "" {
			add("tls.acme requires server.redirect_http (HTTP-01 challenge listener)")
		}
	}
	if c.TLS.SelfSigned {
		sources++
	}
	if c.TLS.Auto {
		sources++
	}
	if sources != 1 {
		add("exactly one TLS source must be configured: tls.cert_file+key_file, tls.acme, tls.self_signed or tls.auto")
	}

	if c.Auth.SessionIdleTimeout.Duration <= 0 || c.Auth.SessionMaxAge.Duration <= 0 {
		add("auth.session_idle_timeout and auth.session_max_age must be positive")
	}
	if c.Auth.LoginRateLimit < 1 || c.Auth.LoginRateWindow.Duration <= 0 {
		add("auth.login_rate_limit must be >= 1 and auth.login_rate_window positive")
	}
	seen := map[string]bool{}
	for i, u := range c.Users {
		if u.Name == "" {
			add("users[%d].name is empty", i)
		}
		if seen[u.Name] {
			add("duplicate user %q", u.Name)
		}
		seen[u.Name] = true
		if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
			add("users[%d] (%s): password_hash must be an argon2id hash from `windstream hash-password`", i, u.Name)
		}
		if c.Auth.RequireTOTP && u.TOTPSecret == "" {
			add("users[%d] (%s): auth.require_totp is set but user has no totp_secret", i, u.Name)
		}
	}

	switch c.Display.Mode {
	case "virtual":
		if strings.TrimSpace(c.Display.VirtualDevice) == "" {
			add("display.virtual_device must name the virtual display adapter")
		}
	case "monitor":
		if strings.TrimSpace(c.Display.Monitor) == "" {
			add("display.monitor must be an index or a \\\\.\\DISPLAYn name")
		}
	case "test":
	default:
		add("display.mode must be virtual, monitor or test")
	}
	if c.Display.Width < 16 || c.Display.Height < 16 {
		add("display.width/height must be set")
	}
	if c.Display.Refresh < 1 {
		add("display.refresh must be >= 1")
	}

	if !validEncoders[c.Video.Encoder] {
		add("video.encoder must be auto, nvenc, amf, qsv or x264")
	}
	switch c.Video.Codec {
	case "", "auto", "av1", "h265", "h264":
	default:
		add("video.codec must be auto, av1, h265 or h264")
	}
	if c.Video.FPS < 1 || c.Video.FPS > 240 {
		add("video.fps must be 1..240")
	}
	if c.Video.BitrateKbps < 500 {
		add("video.bitrate_kbps must be >= 500")
	}
	if c.Video.GOPSeconds <= 0 || c.Video.GOPSeconds > 30 {
		add("video.gop_seconds must be in (0, 30]")
	}
	switch c.Video.Profile {
	case "high", "main", "baseline":
	default:
		add("video.profile must be high, main or baseline")
	}
	if c.Video.GPU < 0 {
		add("video.gpu must be >= 0")
	}
	if c.Audio.Enabled {
		if c.Audio.BitrateKbps < 16 {
			add("audio.bitrate_kbps must be >= 16")
		}
		switch c.Audio.Backend {
		case "wasapi", "test":
		case "dshow":
			if c.Audio.Device == "" {
				add("audio.device is required for the dshow backend")
			}
		default:
			add("audio.backend must be wasapi, dshow or test")
		}
	}
	if c.WebRTC.UDPPort < 1 || c.WebRTC.UDPPort > 65535 {
		add("webrtc.udp_port must be 1..65535")
	}
	if c.WebRTC.TCPPort < 0 || c.WebRTC.TCPPort > 65535 {
		add("webrtc.tcp_port must be 0..65535")
	}
	if c.Network.HTTPSExternalPort < 1 || c.Network.HTTPSExternalPort > 65535 {
		add("network.https_external_port must be 1..65535")
	}
	if c.Input.MaxGamepads < 0 || c.Input.MaxGamepads > 8 {
		add("input.max_gamepads must be 0..8")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level must be debug, info, warn or error")
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		add("log.format must be text or json")
	}
	return errors.Join(errs...)
}

// GOPFrames returns the keyframe interval in frames.
func (v Video) GOPFrames() int {
	g := int(float64(v.FPS)*v.GOPSeconds + 0.5)
	if g < 1 {
		g = 1
	}
	return g
}

// IncludeLoopbackCandidate reports whether loopback ICE candidates should be
// advertised (local testing only).
func (w WebRTC) IncludeLoopbackCandidate() bool { return w.IncludeLoopback }

// ListenPort returns the port of server.listen (0 if unparseable).
func (c Config) ListenPort() int {
	_, p, err := net.SplitHostPort(c.Server.Listen)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return 0
	}
	return n
}

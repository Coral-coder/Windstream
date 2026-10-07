package control

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/coral-coder/windstream/internal/auth"
	"github.com/coral-coder/windstream/internal/config"
	"github.com/coral-coder/windstream/internal/netx"
	"github.com/coral-coder/windstream/web"
)

const panelCookie = "windstream_panel"

// panel is the local dashboard. It listens on loopback only, rejects any
// Host header other than its own address (DNS-rebinding defence), requires
// same-origin JSON for every change (CSRF defence) and, once an account
// exists, a signed-in session.
type panel struct {
	c        *Controller
	sessions *auth.SessionStore
	limiter  *auth.Limiter
	hosts    map[string]bool

	mu      sync.Mutex
	pending map[string]pendingUser // keyed by setup token
}

type pendingUser struct {
	name, hash, secret string
	expires            time.Time
}

func newPanel(c *Controller) *panel {
	_, port, _ := net.SplitHostPort(c.panelAddr)
	return &panel{
		c:        c,
		sessions: auth.NewSessionStore(12*time.Hour, 7*24*time.Hour),
		limiter:  auth.NewLimiter(10, time.Minute),
		hosts:    map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true},
		pending:  map[string]pendingUser{},
	}
}

func (p *panel) run(ctx context.Context) error {
	ln, err := net.Listen("tcp", p.c.panelAddr)
	if err != nil {
		return fmt.Errorf("dashboard port %s is busy: %w", p.c.panelAddr, err)
	}
	srv := &http.Server{Handler: p.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (p *panel) routes() http.Handler {
	mux := http.NewServeMux()
	assets, _ := fs.Sub(web.Admin, "admin")
	files := http.FileServerFS(assets)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /api/state", p.handleState)
	mux.HandleFunc("POST /api/setup/begin", p.handleSetupBegin)
	mux.HandleFunc("POST /api/setup/finish", p.handleSetupFinish)
	mux.HandleFunc("POST /api/login", p.handleLogin)
	mux.HandleFunc("POST /api/logout", p.authed(p.handleLogout))
	mux.HandleFunc("POST /api/settings", p.authed(p.handleSettings))
	mux.HandleFunc("POST /api/users/delete", p.authed(p.handleUserDelete))
	mux.HandleFunc("POST /api/users/signout", p.authed(p.handleUserSignout))
	mux.HandleFunc("POST /api/network/refresh", p.authed(p.handleNetRefresh))
	mux.HandleFunc("POST /api/uninstall", p.authed(p.handleUninstall))
	mux.HandleFunc("POST /api/quit", p.authed(p.handleQuit))
	return p.guard(mux)
}

// guard enforces loopback Host, same-origin JSON writes and safe headers.
func (p *panel) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !p.hosts[strings.ToLower(r.Host)] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if r.Method == http.MethodPost {
			origin := r.Header.Get("Origin")
			if origin == "" || !p.hosts[strings.TrimPrefix(strings.ToLower(origin), "http://")] {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "json required", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (p *panel) session(r *http.Request) (auth.Session, string, bool) {
	ck, err := r.Cookie(panelCookie)
	if err != nil {
		return auth.Session{}, "", false
	}
	s, ok := p.sessions.Get(ck.Value)
	return s, ck.Value, ok
}

func (p *panel) authed(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := p.session(r); !ok {
			jsonErr(w, http.StatusUnauthorized, "Sign in first")
			return
		}
		fn(w, r)
	}
}

func (p *panel) setCookie(w http.ResponseWriter, user string) {
	tok, err := p.sessions.Create(user, "127.0.0.1")
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: panelCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<10)).Decode(v)
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ---- state ----

// Settings is the user-facing subset of the configuration.
type Settings struct {
	Resolution    string `json:"resolution"` // "1920x1080"
	Refresh       int    `json:"refresh"`
	FPS           int    `json:"fps"`
	BitrateMbps   int    `json:"bitrate_mbps"`
	Encoder       string `json:"encoder"`
	Codec         string `json:"codec"`
	DisplayMode   string `json:"display_mode"` // virtual | monitor
	Monitor       string `json:"monitor"`
	MakePrimary   bool   `json:"make_primary"`
	Audio         bool   `json:"audio"`
	Gamepads      bool   `json:"gamepads"`
	KeyboardMouse bool   `json:"keyboard_mouse"`
	LaunchSteam   bool   `json:"launch_steam"`
	UPnP          bool   `json:"upnp"`
	CustomDomain  string `json:"custom_domain"`
	MaxClients    int    `json:"max_clients"`
	HTTPSPort     int    `json:"https_port"`
	MediaPort     int    `json:"media_port"`
}

func settingsFrom(cfg config.Config) Settings {
	return Settings{
		Resolution: fmt.Sprintf("%dx%d", cfg.Display.Width, cfg.Display.Height), Refresh: cfg.Display.Refresh,
		FPS: cfg.Video.FPS, BitrateMbps: cfg.Video.BitrateKbps / 1000, Encoder: cfg.Video.Encoder,
		Codec: cfg.Video.Codec, DisplayMode: cfg.Display.Mode, Monitor: cfg.Display.Monitor, MakePrimary: cfg.Display.MakePrimary,
		Audio: cfg.Audio.Enabled, Gamepads: cfg.Input.Gamepads, KeyboardMouse: cfg.Input.Keyboard && cfg.Input.Mouse,
		LaunchSteam: len(cfg.Display.Launch) > 0 && strings.Contains(strings.ToLower(cfg.Display.Launch[0]), "steam"),
		UPnP:        cfg.Network.UPnP, CustomDomain: cfg.Network.CustomDomain, MaxClients: cfg.Server.MaxClients,
		HTTPSPort: cfg.ListenPort(), MediaPort: cfg.WebRTC.UDPPort,
	}
}

type userInfo struct {
	Name string `json:"name"`
	TOTP bool   `json:"totp"`
}

func (p *panel) handleState(w http.ResponseWriter, r *http.Request) {
	cfg := p.c.Config()
	sess, _, loggedIn := p.session(r)
	setup := len(cfg.Users) == 0
	resp := map[string]any{
		"version":      p.c.opts.Version,
		"setup_needed": setup,
		"logged_in":    loggedIn,
		"dev":          p.c.opts.Dev,
	}
	if !loggedIn {
		jsonOK(w, resp)
		return
	}
	resp["user"] = sess.User
	var users []userInfo
	for _, u := range cfg.Users {
		users = append(users, userInfo{Name: u.Name, TOTP: u.TOTPSecret != ""})
	}
	ns := p.c.netStatus()
	resp["users"] = users
	resp["deps"] = p.c.deps.Snapshot()
	resp["network"] = ns
	resp["stream"] = p.c.stackStatus()
	resp["https_error"] = p.c.httpsError()
	resp["panel_url"] = p.c.PanelURL()
	resp["settings"] = settingsFrom(cfg)
	resp["steam_found"] = p.c.opts.Platform != nil && p.c.opts.Platform.SteamExe() != ""
	resp["lan_fingerprint"] = p.c.tls.SelfSignedFingerprint()
	var monitors []map[string]any
	for _, o := range listOutputs() {
		monitors = append(monitors, map[string]any{"index": o.Index, "name": o.DeviceName, "monitor": o.MonitorName,
			"size": fmt.Sprintf("%dx%d", o.Width, o.Height), "primary": o.Primary})
	}
	resp["monitors"] = monitors
	link := ns.PublicURL
	if link == "" {
		link = ns.LANURL
	}
	resp["link"] = link
	if link != "" {
		if png, err := qrcode.Encode(link, qrcode.Medium, 256); err == nil {
			resp["link_qr"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
	}
	if p.c.opts.Logs != nil {
		lines := p.c.opts.Logs.Lines()
		if len(lines) > 150 {
			lines = lines[len(lines)-150:]
		}
		resp["logs"] = lines
	}
	jsonOK(w, resp)
}

// ---- accounts ----

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// handleSetupBegin validates a new account and returns a TOTP secret + QR.
// Allowed without a session only while no account exists (first run).
func (p *panel) handleSetupBegin(w http.ResponseWriter, r *http.Request) {
	cfg := p.c.Config()
	if len(cfg.Users) > 0 {
		if _, _, ok := p.session(r); !ok {
			jsonErr(w, http.StatusUnauthorized, "Sign in first")
			return
		}
	}
	var req struct{ Username, Password string }
	if err := decode(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "Bad request")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !nameRe.MatchString(req.Username) {
		jsonErr(w, http.StatusBadRequest, "Username: 1-32 letters, digits, dot, dash or underscore")
		return
	}
	for _, u := range cfg.Users {
		if strings.EqualFold(u.Name, req.Username) {
			jsonErr(w, http.StatusBadRequest, "That username already exists")
			return
		}
	}
	if len(req.Password) < 12 || len(req.Password) > 256 {
		jsonErr(w, http.StatusBadRequest, "Password must be at least 12 characters")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "Could not hash password")
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "Could not create secret")
		return
	}
	tokBytes := make([]byte, 24)
	_, _ = rand.Read(tokBytes)
	tok := base64.RawURLEncoding.EncodeToString(tokBytes)
	p.mu.Lock()
	for k, v := range p.pending {
		if time.Now().After(v.expires) {
			delete(p.pending, k)
		}
	}
	p.pending[tok] = pendingUser{name: req.Username, hash: hash, secret: secret, expires: time.Now().Add(15 * time.Minute)}
	p.mu.Unlock()
	url := auth.TOTPURL("Windstream", req.Username, secret)
	png, _ := qrcode.Encode(url, qrcode.Medium, 256)
	jsonOK(w, map[string]string{
		"token": tok, "secret": secret, "otpauth": url,
		"qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	})
}

// handleSetupFinish verifies the first authenticator code and saves the account.
func (p *panel) handleSetupFinish(w http.ResponseWriter, r *http.Request) {
	var req struct{ Token, Code string }
	if err := decode(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "Bad request")
		return
	}
	if !p.limiter.Allow("setup") {
		jsonErr(w, http.StatusTooManyRequests, "Too many attempts, wait a minute")
		return
	}
	p.mu.Lock()
	pu, ok := p.pending[req.Token]
	p.mu.Unlock()
	if !ok || time.Now().After(pu.expires) {
		jsonErr(w, http.StatusBadRequest, "Setup expired, start again")
		return
	}
	if _, ok := auth.ValidateTOTP(pu.secret, req.Code, time.Now(), 1); !ok {
		jsonErr(w, http.StatusBadRequest, "That code didn't match. Check the time on your phone and try the next code.")
		return
	}
	first := false
	err := p.c.update(func(c *config.Config) error {
		first = len(c.Users) == 0
		if !first {
			if _, _, ok := p.session(r); !ok {
				return errors.New("sign in first")
			}
		}
		for _, u := range c.Users {
			if strings.EqualFold(u.Name, pu.name) {
				return errors.New("that username already exists")
			}
		}
		c.Users = append(c.Users, config.User{Name: pu.name, PasswordHash: pu.hash, TOTPSecret: pu.secret})
		return nil
	})
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p.mu.Lock()
	delete(p.pending, req.Token)
	p.mu.Unlock()
	p.c.log.Info("account created", "user", pu.name)
	if first {
		p.setCookie(w, pu.name)
	}
	jsonOK(w, map[string]bool{"ok": true})
}

func (p *panel) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password, Code string }
	if err := decode(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "Bad request")
		return
	}
	if !p.limiter.Allow("login") {
		jsonErr(w, http.StatusTooManyRequests, "Too many attempts, wait a minute")
		return
	}
	switch p.c.auth.Authenticate(strings.TrimSpace(req.Username), req.Password, req.Code) {
	case auth.OK:
		p.setCookie(w, strings.TrimSpace(req.Username))
		jsonOK(w, map[string]bool{"ok": true})
	case auth.TOTPRequired:
		jsonErr(w, http.StatusUnauthorized, "totp_required")
	default:
		jsonErr(w, http.StatusUnauthorized, "Wrong username, password or code")
	}
}

func (p *panel) handleLogout(w http.ResponseWriter, r *http.Request) {
	if _, tok, ok := p.session(r); ok {
		p.sessions.Revoke(tok)
	}
	http.SetCookie(w, &http.Cookie{Name: panelCookie, Value: "", Path: "/", MaxAge: -1})
	jsonOK(w, map[string]bool{"ok": true})
}

func (p *panel) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	if err := decode(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "Bad request")
		return
	}
	err := p.c.update(func(c *config.Config) error {
		if len(c.Users) <= 1 {
			return errors.New("you can't delete the last account")
		}
		out := c.Users[:0]
		for _, u := range c.Users {
			if u.Name != req.Name {
				out = append(out, u)
			}
		}
		c.Users = out
		return nil
	})
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p.c.sessions.RevokeUser(req.Name)
	p.sessions.RevokeUser(req.Name)
	jsonOK(w, map[string]bool{"ok": true})
}

func (p *panel) handleUserSignout(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	if err := decode(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "Bad request")
		return
	}
	p.c.sessions.RevokeUser(req.Name)
	jsonOK(w, map[string]bool{"ok": true})
}

// ---- settings ----

func (p *panel) handleSettings(w http.ResponseWriter, r *http.Request) {
	var s Settings
	if err := decode(r, &s); err != nil {
		jsonErr(w, http.StatusBadRequest, "Bad request")
		return
	}
	var width, height int
	if _, err := fmt.Sscanf(s.Resolution, "%dx%d", &width, &height); err != nil || width < 640 || height < 480 || width > 7680 || height > 4320 {
		jsonErr(w, http.StatusBadRequest, "Resolution must look like 1920x1080")
		return
	}
	if s.BitrateMbps < 2 || s.BitrateMbps > 150 {
		jsonErr(w, http.StatusBadRequest, "Bitrate must be 2-150 Mbps")
		return
	}
	s.CustomDomain = strings.ToLower(strings.TrimSpace(s.CustomDomain))
	if s.CustomDomain != "" && !regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+$`).MatchString(s.CustomDomain) {
		jsonErr(w, http.StatusBadRequest, "Custom domain must be a hostname like games.example.com")
		return
	}
	cur := p.c.Config()
	if s.HTTPSPort == 0 {
		s.HTTPSPort = cur.ListenPort()
	}
	if s.MediaPort == 0 {
		s.MediaPort = cur.WebRTC.UDPPort
	}
	if s.HTTPSPort < 1 || s.HTTPSPort > 65535 || s.MediaPort < 1 || s.MediaPort > 65535 {
		jsonErr(w, http.StatusBadRequest, "Ports must be between 1 and 65535")
		return
	}
	if s.HTTPSPort == cur.Server.PanelPort {
		jsonErr(w, http.StatusBadRequest, fmt.Sprintf("Port %d is used by this dashboard; pick another", s.HTTPSPort))
		return
	}
	httpsChanged := s.HTTPSPort != cur.ListenPort()
	mediaChanged := s.MediaPort != cur.WebRTC.UDPPort
	if httpsChanged && !netx.TCPFree(s.HTTPSPort) {
		jsonErr(w, http.StatusBadRequest, fmt.Sprintf("TCP port %d is already in use by another program", s.HTTPSPort))
		return
	}
	if mediaChanged && !netx.UDPFree(s.MediaPort) {
		jsonErr(w, http.StatusBadRequest, fmt.Sprintf("UDP port %d is already in use by another program", s.MediaPort))
		return
	}
	steam := ""
	if p.c.opts.Platform != nil {
		steam = p.c.opts.Platform.SteamExe()
	}
	netChanged := httpsChanged || mediaChanged
	err := p.c.update(func(c *config.Config) error {
		c.Display.Width, c.Display.Height, c.Display.Refresh = width, height, s.Refresh
		c.Video.FPS, c.Video.BitrateKbps, c.Video.Encoder = s.FPS, s.BitrateMbps*1000, s.Encoder
		if s.Codec != "" {
			c.Video.Codec = s.Codec
		}
		if !p.c.opts.Dev {
			c.Display.Mode = s.DisplayMode
		}
		c.Display.Monitor, c.Display.MakePrimary = s.Monitor, s.MakePrimary
		if c.Display.Monitor == "" {
			c.Display.Monitor = "0"
		}
		c.Audio.Enabled, c.Input.Gamepads = s.Audio, s.Gamepads
		c.Input.Keyboard, c.Input.Mouse = s.KeyboardMouse, s.KeyboardMouse
		c.Display.Launch = nil
		if s.LaunchSteam && steam != "" {
			c.Display.Launch = []string{steam, "steam://open/bigpicture"}
		}
		netChanged = netChanged || c.Network.UPnP != s.UPnP || c.Network.CustomDomain != s.CustomDomain
		c.Network.UPnP, c.Network.CustomDomain = s.UPnP, s.CustomDomain
		c.Server.Listen = fmt.Sprintf(":%d", s.HTTPSPort)
		c.WebRTC.UDPPort = s.MediaPort
		if s.MaxClients >= 1 && s.MaxClients <= 8 {
			c.Server.MaxClients = s.MaxClients
		}
		return nil
	})
	if err != nil {
		jsonErr(w, http.StatusBadRequest, trimErr(err))
		return
	}
	if httpsChanged {
		p.c.restartHTTPS()
	}
	if netChanged {
		p.c.restartNetwork()
	}
	p.c.requestRestart()
	jsonOK(w, map[string]bool{"ok": true})
}

func (p *panel) handleNetRefresh(w http.ResponseWriter, r *http.Request) {
	p.c.restartNetwork()
	jsonOK(w, map[string]bool{"ok": true})
}

func (p *panel) handleUninstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RemoveData bool `json:"remove_data"`
	}
	_ = decode(r, &req)
	if p.c.opts.Platform == nil {
		jsonErr(w, http.StatusBadRequest, "Uninstall is only available in the installed Windows app")
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := p.c.opts.Platform.Uninstall(req.RemoveData); err != nil {
			p.c.log.Error("uninstall failed", "error", err)
		}
	}()
}

func (p *panel) handleQuit(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]bool{"ok": true})
	if p.c.opts.Platform != nil {
		go func() { time.Sleep(300 * time.Millisecond); p.c.opts.Platform.Quit() }()
	}
}

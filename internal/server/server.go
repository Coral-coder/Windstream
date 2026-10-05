// Package server exposes Windstream over HTTPS: static client, login API and
// the WebSocket signaling endpoint.
package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/coral-coder/windstream/internal/auth"
	"github.com/coral-coder/windstream/internal/config"
	"github.com/coral-coder/windstream/internal/stream"
	"github.com/coral-coder/windstream/web"
)

const sessionCookie = "__Host-windstream"

// Server is the HTTPS front end.
type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	auth     *auth.Authenticator
	sessions *auth.SessionStore
	limiter  *auth.Limiter
	hub      *stream.Hub
	handler  http.Handler
	tlsCfg   *tls.Config
	acme     *autocert.Manager
	origins  map[string]bool
}

// New wires the handlers and TLS configuration.
func New(cfg *config.Config, hub *stream.Hub, log *slog.Logger) (*Server, error) {
	users := make([]auth.User, 0, len(cfg.Users))
	for _, u := range cfg.Users {
		users = append(users, auth.User{Name: u.Name, PasswordHash: u.PasswordHash, TOTPSecret: u.TOTPSecret})
	}
	s := &Server{
		cfg:      cfg,
		log:      log,
		auth:     auth.NewAuthenticator(users, cfg.Auth.RequireTOTP),
		sessions: auth.NewSessionStore(cfg.Auth.SessionIdleTimeout.Duration, cfg.Auth.SessionMaxAge.Duration),
		limiter:  auth.NewLimiter(cfg.Auth.LoginRateLimit, cfg.Auth.LoginRateWindow.Duration),
		hub:      hub,
		origins:  make(map[string]bool),
	}
	for _, o := range cfg.Server.AllowedOrigins {
		s.origins[strings.ToLower(strings.TrimSuffix(o, "/"))] = true
	}
	if err := s.setupTLS(); err != nil {
		return nil, err
	}
	s.handler = s.routes()
	return s, nil
}

func (s *Server) setupTLS() error {
	s.tlsCfg = baseTLSConfig()
	switch {
	case s.cfg.TLS.ACME:
		s.acme = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(s.cfg.TLS.ACMEDomains...),
			Cache:      autocert.DirCache(s.cfg.TLS.ACMECacheDir),
			Email:      s.cfg.TLS.ACMEEmail,
		}
		s.tlsCfg.GetCertificate = s.acme.GetCertificate
		s.tlsCfg.NextProtos = append(s.tlsCfg.NextProtos, "acme-tls/1")
	case s.cfg.TLS.CertFile != "":
		r, err := newCertReloader(s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile)
		if err != nil {
			return err
		}
		s.tlsCfg.GetCertificate = r.get
	case s.cfg.TLS.SelfSigned:
		hosts := []string{"localhost", "127.0.0.1", "::1"}
		if hn, err := os.Hostname(); err == nil {
			hosts = append(hosts, hn)
		}
		certPEM, keyPEM, err := GenerateSelfSigned(hosts, 365*24*time.Hour)
		if err != nil {
			return err
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return err
		}
		s.tlsCfg.Certificates = []tls.Certificate{cert}
		s.log.Warn("using an ephemeral self-signed certificate; browsers will warn. Use tls.cert_file or tls.acme in production.",
			"sha256", Fingerprint(&cert))
	default:
		return errors.New("no TLS source configured")
	}
	return nil
}

// Handler returns the HTTP handler (exposed for tests).
func (s *Server) Handler() http.Handler { return s.handler }

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Server.Listen,
		Handler:           s.handler,
		TLSConfig:         s.tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	errCh := make(chan error, 2)
	ln, err := net.Listen("tcp", s.cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Server.Listen, err)
	}
	s.log.Info("https listening", "addr", ln.Addr().String(), "open", "https://<this-pc-or-domain>"+portSuffix(ln.Addr()))
	go func() { errCh <- srv.ServeTLS(ln, "", "") }()

	var redirect *http.Server
	if s.cfg.Server.RedirectHTTP != "" {
		var h http.Handler = http.HandlerFunc(redirectHTTPS)
		if s.acme != nil {
			h = s.acme.HTTPHandler(h)
		}
		redirect = &http.Server{
			Addr: s.cfg.Server.RedirectHTTP, Handler: h,
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
		}
		s.log.Info("http redirect listening", "addr", s.cfg.Server.RedirectHTTP)
		go func() { errCh <- redirect.ListenAndServe() }()
	}

	go s.janitor(ctx)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if redirect != nil {
		_ = redirect.Shutdown(shutdownCtx)
	}
	return nil
}

func (s *Server) janitor(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sessions.Sweep()
			s.limiter.Sweep()
		}
	}
}

func redirectHTTPS(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	w.Header().Set("Connection", "close")
	http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("GET /api/signal", s.handleSignal)
	mux.Handle("GET /", s.staticHandler())
	return s.securityHeaders(mux)
}

// staticHandler serves the embedded client. Only known files are served and
// everything else is a 404, so no directory listings or source maps leak.
func (s *Server) staticHandler() http.Handler {
	files := map[string][]byte{}
	for _, name := range web.Names() {
		data, err := fs.ReadFile(web.Files, name)
		if err != nil {
			panic(err)
		}
		files[name] = data
	}
	started := time.Now()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		data, ok := files[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if path == "index.html" {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		http.ServeContent(w, r, path, started, bytes.NewReader(data))
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
			"media-src 'self' blob: mediastream:; connect-src 'self' wss://"+r.Host+"; font-src 'self'; "+
			"frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin enforces that state-changing requests and WebSocket upgrades
// come from this site (or an explicitly allowed origin). Combined with the
// SameSite=Strict cookie this closes CSRF and cross-site WebSocket hijacking.
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := strings.ToLower(r.Header.Get("Origin"))
	if origin == "" {
		// Browsers always send Origin on POST and WebSocket upgrades; its
		// absence means a non-browser client, which is fine for the API as
		// long as it presents a valid cookie (fetch metadata still applies).
		return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "none"
	}
	if s.origins[origin] {
		return true
	}
	return origin == "https://"+strings.ToLower(r.Host)
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.Server.BehindProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				xff = xff[:i]
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) session(r *http.Request) (auth.Session, string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return auth.Session{}, "", false
	}
	sess, ok := s.sessions.Get(c.Value)
	return sess, c.Value, ok
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func portSuffix(a net.Addr) string {
	if t, ok := a.(*net.TCPAddr); ok && t.Port != 443 {
		return fmt.Sprintf(":%d", t.Port)
	}
	return ""
}

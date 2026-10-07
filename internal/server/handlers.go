package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/coral-coder/windstream/internal/auth"
	"github.com/coral-coder/windstream/internal/stream"

	"github.com/coral-coder/windstream/internal/safe"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad_origin")
		return
	}
	ip := s.clientIP(r)
	if !s.limiter.Allow(ip) {
		s.log.Warn("login rate limited", "ip", ip)
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) > 64 || len(req.Password) > 1024 || len(req.Code) > 16 {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	switch s.auth.Authenticate(req.Username, req.Password, req.Code) {
	case auth.OK:
	case auth.TOTPRequired:
		writeError(w, http.StatusUnauthorized, "totp_required")
		return
	default:
		s.log.Warn("login failed", "user", req.Username, "ip", ip)
		// Keep failure latency uniform regardless of which check failed.
		time.Sleep(250 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	token, err := s.sessions.Create(req.Username, ip)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(s.cfg.Auth.SessionMaxAge.Duration.Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	s.log.Info("login ok", "user", req.Username, "ip", ip)
	writeJSON(w, http.StatusOK, map[string]any{"user": req.Username})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad_origin")
		return
	}
	if _, token, ok := s.session(r); ok {
		s.sessions.Revoke(token)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, _, ok := s.session(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	resp := map[string]any{"user": sess.User}
	if h := s.hub.Load(); h != nil {
		resp["stream"] = h.Stats()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSignal(w http.ResponseWriter, r *http.Request) {
	sess, token, ok := s.session(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad_origin")
		return
	}
	// Origin was already verified above, so skip the library's host-only check
	// (which would reject configured allowed_origins).
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		s.log.Debug("websocket accept", "error", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(128 * 1024)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	send := func(m stream.SignalMessage) error {
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		defer wcancel()
		return wsjson.Write(wctx, conn, m)
	}
	hub := s.hub.Load()
	if hub == nil {
		_ = send(stream.SignalMessage{Type: "error", Message: "The streaming engine is starting up. Retrying…"})
		conn.Close(websocket.StatusTryAgainLater, "starting")
		return
	}
	client, err := hub.Connect(sess.User, s.clientIP(r), send)
	if err != nil {
		s.log.Warn("stream connect refused", "user", sess.User, "error", err)
		_ = send(stream.SignalMessage{Type: "error", Message: err.Error()})
		conn.Close(websocket.StatusPolicyViolation, "refused")
		return
	}
	defer client.Close()

	// Close the socket when the peer connection dies or the session is revoked.
	go func() {
		defer safe.Recover(s.log, "signaling watchdog")
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-client.Done():
				conn.Close(websocket.StatusNormalClosure, "peer closed")
				return
			case <-t.C:
				if _, ok := s.sessions.Get(token); !ok {
					s.log.Info("session expired mid-stream; closing", "user", sess.User)
					conn.Close(websocket.StatusPolicyViolation, "session expired")
					return
				}
			}
		}
	}()

	for {
		var m stream.SignalMessage
		if err := wsjson.Read(ctx, conn, &m); err != nil {
			var ce websocket.CloseError
			if !errors.As(err, &ce) && ctx.Err() == nil {
				s.log.Debug("signaling read ended", "error", err)
			}
			return
		}
		if err := client.HandleSignal(m); err != nil {
			s.log.Warn("signaling error", "type", m.Type, "error", err)
			if m.Type == "hello" {
				_ = send(stream.SignalMessage{Type: "error", Message: helloErrorMessage(err)})
				conn.Close(websocket.StatusUnsupportedData, "negotiation failed")
				return
			}
			if m.Type == "answer" {
				// A rejected answer (the browser cannot decode the codec)
				// cannot recover by retrying, so tell the client and stop.
				_ = send(stream.SignalMessage{Type: "error", Message: friendlySignalError(err)})
				conn.Close(websocket.StatusUnsupportedData, "negotiation failed")
				return
			}
			_ = send(stream.SignalMessage{Type: "error", Message: "signaling: " + err.Error()})
		}
	}
}

func helloErrorMessage(err error) string {
	if errors.Is(err, stream.ErrNoCodec) {
		return "This browser cannot decode any video format this PC can encode (AV1, HEVC or H.264). Use a current Chrome, Edge, Safari or Firefox."
	}
	return "Could not start the stream: " + err.Error()
}

func friendlySignalError(err error) string {
	if strings.Contains(err.Error(), "codec is not supported") {
		return "This browser cannot decode the video format being streamed. Use a current Chrome, Edge, Safari or Firefox."
	}
	return "Media negotiation failed: " + err.Error()
}

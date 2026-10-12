package netx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// Ports describes what must be reachable from the internet.
type Ports struct {
	HTTPSInternal uint16 // local HTTPS listener
	HTTPSExternal uint16 // preferred public port (443 lets Let's Encrypt validate)
	MediaUDP      uint16 // WebRTC media, same port inside and out
	MediaTCP      uint16 // optional ICE-TCP fallback (0 = off)
}

// Status is shown on the dashboard.
type Status struct {
	UPnP          string   `json:"upnp"`           // "mapped", "unavailable", "disabled", "error"
	UPnPDetail    string   `json:"upnp_detail"`    // human-readable
	PublicIP      string   `json:"public_ip"`      // may be empty
	CGNAT         bool     `json:"cgnat"`          // ISP shares the IP; inbound impossible
	LANIP         string   `json:"lan_ip"`         //
	Hostname      string   `json:"hostname"`       // DNS name used for the certificate
	HTTPSExternal uint16   `json:"https_external"` // public HTTPS port actually mapped/expected
	PublicURL     string   `json:"public_url"`     //
	LANURL        string   `json:"lan_url"`        //
	Forward       []string `json:"forward"`        // manual port-forward instructions if UPnP failed
	Checked       string   `json:"checked"`
	// PublicCheck is "ok" when a request to PublicURL reached this
	// Windstream, "failed" when something else answered (or nothing did),
	// and "" when it was not checked.
	PublicCheck       string `json:"public_check"`
	PublicCheckDetail string `json:"public_check_detail,omitempty"`
	// PublicCheckForeign: the check reached a different server (e.g. the
	// router's own page), so the public link cannot work.
	PublicCheckForeign bool `json:"public_check_foreign,omitempty"`
	// SeenFromOutside: a device on the internet reached this PC through
	// PublicURL recently, so it works whatever the PC's own check says
	// (many routers cannot loop their public address back inside the home).
	SeenFromOutside bool `json:"seen_from_outside,omitempty"`
}

// Link is the address to hand out: the public one unless it is known not to
// reach this PC, otherwise the home-network one. With the router ports
// opened, a check that got no answer most likely means the router cannot
// loop back to its own public address, which only affects devices inside
// the home; a check answered by another server means the link is broken.
func (s Status) Link() string {
	if s.PublicURL == "" || s.LANURL == "" {
		return s.PublicURL + s.LANURL
	}
	if s.PublicCheck != "failed" || (s.UPnP == "mapped" && !s.PublicCheckForeign) {
		return s.PublicURL
	}
	return s.LANURL
}

// Manager keeps the router mappings and public address current.
type Manager struct {
	log          *slog.Logger
	ports        Ports
	upnp         bool
	customDomain string
	stunServer   string
	discover     func(context.Context) (*Gateway, error) // DiscoverGateway (tests replace it)
	onChange     func(Status)
	// verify checks that a public URL reaches this server.
	verify func(ctx context.Context, url string) error

	refreshMu sync.Mutex // one Refresh at a time
	// avoidPreferred: something other than this PC answers on the preferred
	// public HTTPS port (usually the router itself on 443), so the internal
	// port is used publicly instead.
	avoidPreferred bool
	mu             sync.Mutex
	outsideHost    string    // hostname a device on the internet last used
	outsideAt      time.Time // when
	status         Status
	gw             *Gateway
	mapped         []mapping
}

type mapping struct {
	proto string
	ext   uint16
}

// NewManager creates a manager. onChange is called whenever the public IP or
// hostname changes.
func NewManager(log *slog.Logger, ports Ports, useUPnP bool, customDomain string, onChange func(Status)) *Manager {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	m := &Manager{log: log, ports: ports, upnp: useUPnP, customDomain: customDomain,
		stunServer: "stun.l.google.com:19302", onChange: onChange, discover: DiscoverGateway}
	// The home-network address is known immediately; the public one follows.
	if lan := LANIP(); lan != nil {
		m.status.LANIP = lan.String()
		m.status.LANURL = fmt.Sprintf("https://%s:%d", lan, ports.HTTPSInternal)
	}
	return m
}

// SetVerifier sets the check run against the public URL on every refresh.
// Call it before Run.
func (m *Manager) SetVerifier(verify func(ctx context.Context, url string) error) {
	m.verify = verify
}

// outsideValid is how long a visit from the internet vouches for the link.
const outsideValid = 24 * time.Hour

// ConfirmFromOutside records that a device on the internet reached this PC
// using host. If that is the public link's hostname, the link is marked as
// working.
func (m *Manager) ConfirmFromOutside(host string) {
	m.mu.Lock()
	if !strings.EqualFold(host, m.status.Hostname) || host == "" {
		m.mu.Unlock()
		return
	}
	first := time.Since(m.outsideAt) > outsideValid || !strings.EqualFold(m.outsideHost, host)
	m.outsideHost, m.outsideAt = host, time.Now()
	changed := !m.status.SeenFromOutside
	if changed {
		m.status = applyOutside(m.status)
	}
	st := m.status
	m.mu.Unlock()
	if first {
		m.log.Info("the public link was used from outside the home network; it works", "url", st.PublicURL)
	}
	if changed && m.onChange != nil {
		m.onChange(st)
	}
}

// applyOutside marks st as confirmed by a visit from the internet.
func applyOutside(st Status) Status {
	st.SeenFromOutside = true
	if st.PublicCheck == "failed" {
		st.PublicCheck, st.PublicCheckForeign = "ok", false
		st.PublicCheckDetail = "This PC can't open its own internet link (the router doesn't loop it back), but it was used from outside your home, so it works."
	}
	return st
}

// Status returns the latest status.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Run refreshes every 20 minutes (renewing UPnP leases) until ctx ends, then
// removes the mappings it created.
func (m *Manager) Run(ctx context.Context) {
	defer m.unmapAll()
	m.Refresh(ctx)
	failures := 0
	for {
		// While the public link does not work yet, look again soon: the
		// user may be adding port forwards right now.
		wait := 20 * time.Minute
		if m.Status().PublicCheck == "failed" && failures < 10 {
			failures++
			wait = 2 * time.Minute
		} else if m.Status().PublicCheck != "failed" {
			failures = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			m.Refresh(ctx)
		}
	}
}

// Refresh re-detects addresses and re-applies port mappings.
func (m *Manager) Refresh(ctx context.Context) {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	prev := m.Status()
	st := Status{UPnP: "disabled", HTTPSExternal: m.ports.HTTPSExternal, Checked: time.Now().Format(time.RFC3339)}
	if lan := LANIP(); lan != nil {
		st.LANIP = lan.String()
		st.LANURL = fmt.Sprintf("https://%s:%d", lan, m.ports.HTTPSInternal)
	}
	var public net.IP
	var gw *Gateway
	if m.upnp {
		var err error
		gw, err = m.discover(ctx)
		if err != nil {
			st.UPnP, st.UPnPDetail = "unavailable", err.Error()
		} else {
			if ip, err := gw.ExternalIP(ctx); err == nil {
				public = ip
			}
			ext, err := m.mapAll(ctx, gw)
			if err != nil {
				st.UPnP, st.UPnPDetail = "error", err.Error()
				// A refused renewal leaves the earlier mapping in place for
				// a while; keep pointing at the port that worked rather than
				// at one the router keeps for itself.
				switch {
				case prev.UPnP == "mapped" && prev.HTTPSExternal != 0:
					st.HTTPSExternal = prev.HTTPSExternal
				case m.avoidPreferred:
					st.HTTPSExternal = m.ports.HTTPSInternal
				}
			} else {
				st.UPnP, st.UPnPDetail = "mapped", "Router ports opened automatically ("+gw.Kind+")"
				st.HTTPSExternal = ext
			}
		}
	}
	if public == nil || !IsPublic(public) {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if ip, err := STUNPublicIP(sctx, m.stunServer); err == nil {
			if public != nil && IsCGNAT(public) {
				st.CGNAT = true
			}
			public = ip
		}
		cancel()
	}
	if public != nil {
		st.PublicIP = public.String()
		if IsCGNAT(public) {
			st.CGNAT = true
		}
	}
	switch {
	case m.customDomain != "":
		st.Hostname = m.customDomain
	case public != nil && IsPublic(public):
		st.Hostname = SSLIPHost(public)
	}
	if st.Hostname != "" {
		st.PublicURL = publicURL(st.Hostname, st.HTTPSExternal)
	}
	if st.PublicURL != "" && m.verify != nil {
		err := m.check(ctx, st.PublicURL)
		var foreign *ForeignAnswerError
		if errors.As(err, &foreign) && st.UPnP == "mapped" && gw != nil && st.HTTPSExternal != m.ports.HTTPSInternal {
			// The router (or something behind it) keeps this public port
			// for itself even though it accepted the mapping. Use the
			// PC's own port publicly instead and check again.
			m.logWarn("another server answers on the public HTTPS port (likely the router itself); moving the link to another port",
				"port", st.HTTPSExternal, "now", m.ports.HTTPSInternal, "answer", err)
			m.avoidPreferred = true
			m.unmapOne(gw, "TCP", st.HTTPSExternal)
			if ext, merr := m.mapAll(ctx, gw); merr == nil {
				st.HTTPSExternal = ext
				st.PublicURL = publicURL(st.Hostname, ext)
				err = m.check(ctx, st.PublicURL)
			}
		}
		switch {
		case err == nil:
			st.PublicCheck = "ok"
		default:
			st.PublicCheck, st.PublicCheckDetail = "failed", err.Error()
			st.PublicCheckForeign = errors.As(err, &foreign)
			// Logged when the outcome changes, not on every re-check.
			if prev.PublicCheckDetail != st.PublicCheckDetail || prev.PublicURL != st.PublicURL {
				m.logWarn("this PC cannot open its own public link (it may still work from outside the home)",
					"url", st.PublicURL, "error", err)
			}
		}
	}
	m.mu.Lock()
	if st.Hostname != "" && strings.EqualFold(m.outsideHost, st.Hostname) && time.Since(m.outsideAt) < outsideValid {
		st = applyOutside(st)
	}
	m.mu.Unlock()
	if st.UPnP != "mapped" || st.PublicCheck == "failed" {
		st.Forward = []string{
			fmt.Sprintf("TCP %d → %s port %d (HTTPS)", st.HTTPSExternal, st.LANIP, m.ports.HTTPSInternal),
			fmt.Sprintf("UDP %d → %s port %d (video, audio, input)", m.ports.MediaUDP, st.LANIP, m.ports.MediaUDP),
		}
	}

	m.mu.Lock()
	prev = m.status
	m.status = st
	m.mu.Unlock()
	if prev.PublicIP != st.PublicIP || prev.Hostname != st.Hostname || prev.HTTPSExternal != st.HTTPSExternal ||
		prev.PublicCheck != st.PublicCheck {
		m.log.Info("network", "public_ip", st.PublicIP, "hostname", st.Hostname, "upnp", st.UPnP, "detail", st.UPnPDetail)
		if m.onChange != nil {
			m.onChange(st)
		}
	}
}

func publicURL(host string, port uint16) string {
	if port == 443 {
		return "https://" + host
	}
	return fmt.Sprintf("https://%s:%d", host, port)
}

func (m *Manager) check(ctx context.Context, url string) error {
	vctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return m.verify(vctx, url)
}

func (m *Manager) logWarn(msg string, args ...any) {
	if m.log != nil {
		m.log.Warn(msg, args...)
	}
}

// unmapOne removes one mapping this manager made.
func (m *Manager) unmapOne(gw *Gateway, proto string, port uint16) {
	m.mu.Lock()
	kept := m.mapped[:0]
	for _, mp := range m.mapped {
		if mp.proto != proto || mp.ext != port {
			kept = append(kept, mp)
		}
	}
	m.mapped = kept
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = gw.Unmap(ctx, proto, port)
}

// mapAll applies the mappings and returns the external HTTPS port used.
func (m *Manager) mapAll(ctx context.Context, gw *Gateway) (uint16, error) {
	m.mu.Lock()
	m.gw = gw
	m.mu.Unlock()
	// Every mapping is recorded the moment it exists, so a later failure
	// (or quitting mid-way) never leaves one behind on the router.
	record := func(proto string, port uint16) {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, mp := range m.mapped {
			if mp.proto == proto && mp.ext == port {
				return
			}
		}
		m.mapped = append(m.mapped, mapping{proto, port})
	}
	// Some routers refuse to renew a mapping that already exists (UPnP
	// error 606/718). If the router still forwards the port to this PC,
	// that is as good as renewed.
	mapOne := func(proto string, ext, in uint16, desc string) error {
		err := gw.Map(ctx, proto, ext, in, desc)
		if err != nil && gw.HasMapping(ctx, proto, ext, in) {
			return nil
		}
		return err
	}
	ext := m.ports.HTTPSExternal
	if m.avoidPreferred {
		ext = m.ports.HTTPSInternal
	}
	err := mapOne("TCP", ext, m.ports.HTTPSInternal, "Windstream HTTPS")
	if err != nil && ext != m.ports.HTTPSInternal {
		// Public 443 kept by the router or another device: use the PC's own
		// port publicly, and stop asking for 443 on every refresh.
		refused := err
		ext = m.ports.HTTPSInternal
		if err = mapOne("TCP", ext, m.ports.HTTPSInternal, "Windstream HTTPS"); err == nil {
			m.log.Info("the router keeps the standard HTTPS port for itself; using another port for the public link",
				"wanted", m.ports.HTTPSExternal, "using", ext, "router_said", shortUPnPErr(refused))
			m.avoidPreferred = true
		}
	}
	if err != nil {
		return 0, fmt.Errorf("map TCP %d: %w", ext, err)
	}
	record("TCP", ext)
	if err := mapOne("UDP", m.ports.MediaUDP, m.ports.MediaUDP, "Windstream media"); err != nil {
		return 0, fmt.Errorf("map UDP %d: %w", m.ports.MediaUDP, err)
	}
	record("UDP", m.ports.MediaUDP)
	if m.ports.MediaTCP != 0 {
		if err := mapOne("TCP", m.ports.MediaTCP, m.ports.MediaTCP, "Windstream media (TCP)"); err == nil {
			record("TCP", m.ports.MediaTCP)
		}
	}
	return ext, nil
}

func (m *Manager) unmapAll() {
	m.mu.Lock()
	gw, maps := m.gw, m.mapped
	m.mapped = nil
	m.mu.Unlock()
	if gw == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, mp := range maps {
		_ = gw.Unmap(ctx, mp.proto, mp.ext)
	}
}

// RemoveMappings deletes Windstream's router mappings (used by uninstall,
// when no Manager is running).
func RemoveMappings(ctx context.Context, ports Ports) {
	gw, err := DiscoverGateway(ctx)
	if err != nil {
		return
	}
	for _, mp := range []mapping{{"TCP", ports.HTTPSExternal}, {"TCP", ports.HTTPSInternal}, {"UDP", ports.MediaUDP}, {"TCP", ports.MediaTCP}} {
		if mp.ext != 0 {
			_ = gw.Unmap(ctx, mp.proto, mp.ext)
		}
	}
}

// shortUPnPErr turns a SOAP fault into its UPnP error code and name.
func shortUPnPErr(err error) string {
	s := err.Error()
	code, desc := between(s, "<errorCode>", "</errorCode>"), between(s, "<errorDescription>", "</errorDescription>")
	if code == "" {
		return s
	}
	return code + " " + desc
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	if j := strings.Index(s, b); j >= 0 {
		return s[:j]
	}
	return ""
}

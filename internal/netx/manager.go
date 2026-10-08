package netx

import (
	"context"
	"fmt"
	"log/slog"
	"net"
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
}

// Link is the address to hand out: the public one unless it is known not to
// reach this PC, otherwise the home-network one. With the router ports
// opened, a failed check most likely means the router cannot loop back to
// its own public address, which only affects devices inside the home.
func (s Status) Link() string {
	if s.PublicURL != "" && (s.PublicCheck != "failed" || s.UPnP == "mapped") {
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
	onChange     func(Status)
	// verify checks that a public URL reaches this server.
	verify func(ctx context.Context, url string) error

	refreshMu sync.Mutex // one Refresh at a time
	mu        sync.Mutex
	status    Status
	gw        *Gateway
	mapped    []mapping
}

type mapping struct {
	proto string
	ext   uint16
}

// NewManager creates a manager. onChange is called whenever the public IP or
// hostname changes.
func NewManager(log *slog.Logger, ports Ports, useUPnP bool, customDomain string, onChange func(Status)) *Manager {
	m := &Manager{log: log, ports: ports, upnp: useUPnP, customDomain: customDomain,
		stunServer: "stun.l.google.com:19302", onChange: onChange}
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
	st := Status{UPnP: "disabled", HTTPSExternal: m.ports.HTTPSExternal, Checked: time.Now().Format(time.RFC3339)}
	if lan := LANIP(); lan != nil {
		st.LANIP = lan.String()
		st.LANURL = fmt.Sprintf("https://%s:%d", lan, m.ports.HTTPSInternal)
	}
	var public net.IP
	if m.upnp {
		gw, err := DiscoverGateway(ctx)
		if err != nil {
			st.UPnP, st.UPnPDetail = "unavailable", err.Error()
		} else {
			if ip, err := gw.ExternalIP(ctx); err == nil {
				public = ip
			}
			ext, err := m.mapAll(ctx, gw)
			if err != nil {
				st.UPnP, st.UPnPDetail = "error", err.Error()
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
		st.PublicURL = "https://" + st.Hostname
		if st.HTTPSExternal != 443 {
			st.PublicURL += fmt.Sprintf(":%d", st.HTTPSExternal)
		}
	}
	if st.PublicURL != "" && m.verify != nil {
		vctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		if err := m.verify(vctx, st.PublicURL); err != nil {
			st.PublicCheck, st.PublicCheckDetail = "failed", err.Error()
			if m.log != nil {
				m.log.Warn("the public link does not reach this PC from here", "url", st.PublicURL, "error", err)
			}
		} else {
			st.PublicCheck = "ok"
		}
		cancel()
	}
	if st.UPnP != "mapped" || st.PublicCheck == "failed" {
		st.Forward = []string{
			fmt.Sprintf("TCP %d → %s port %d (HTTPS)", st.HTTPSExternal, st.LANIP, m.ports.HTTPSInternal),
			fmt.Sprintf("UDP %d → %s port %d (video, audio, input)", m.ports.MediaUDP, st.LANIP, m.ports.MediaUDP),
		}
	}

	m.mu.Lock()
	prev := m.status
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
	ext := m.ports.HTTPSExternal
	err := gw.Map(ctx, "TCP", ext, m.ports.HTTPSInternal, "Windstream HTTPS")
	if err != nil && ext != m.ports.HTTPSInternal {
		// Public 443 taken by another device: fall back to the internal port.
		if m.log != nil {
			m.log.Warn("could not map public HTTPS port; trying fallback", "port", ext, "error", err)
		}
		ext = m.ports.HTTPSInternal
		err = gw.Map(ctx, "TCP", ext, m.ports.HTTPSInternal, "Windstream HTTPS")
	}
	if err != nil {
		return 0, fmt.Errorf("map TCP %d: %w", ext, err)
	}
	record("TCP", ext)
	if err := gw.Map(ctx, "UDP", m.ports.MediaUDP, m.ports.MediaUDP, "Windstream media"); err != nil {
		return 0, fmt.Errorf("map UDP %d: %w", m.ports.MediaUDP, err)
	}
	record("UDP", m.ports.MediaUDP)
	if m.ports.MediaTCP != 0 {
		if err := gw.Map(ctx, "TCP", m.ports.MediaTCP, m.ports.MediaTCP, "Windstream media (TCP)"); err == nil {
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

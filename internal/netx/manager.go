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
}

// Manager keeps the router mappings and public address current.
type Manager struct {
	log          *slog.Logger
	ports        Ports
	upnp         bool
	customDomain string
	stunServer   string
	onChange     func(Status)

	mu     sync.Mutex
	status Status
	gw     *Gateway
	mapped []mapping
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

// Status returns the latest status.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Run refreshes every 20 minutes (renewing UPnP leases) until ctx ends, then
// removes the mappings it created.
func (m *Manager) Run(ctx context.Context) {
	m.Refresh(ctx)
	t := time.NewTicker(20 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.unmapAll()
			return
		case <-t.C:
			m.Refresh(ctx)
		}
	}
}

// Refresh re-detects addresses and re-applies port mappings.
func (m *Manager) Refresh(ctx context.Context) {
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
	if st.UPnP != "mapped" {
		st.Forward = []string{
			fmt.Sprintf("TCP %d → %s port %d (HTTPS)", m.ports.HTTPSExternal, st.LANIP, m.ports.HTTPSInternal),
			fmt.Sprintf("UDP %d → %s port %d (video, audio, input)", m.ports.MediaUDP, st.LANIP, m.ports.MediaUDP),
		}
	}

	m.mu.Lock()
	prev := m.status
	m.status = st
	m.mu.Unlock()
	if prev.PublicIP != st.PublicIP || prev.Hostname != st.Hostname || prev.HTTPSExternal != st.HTTPSExternal {
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
	maps := []mapping{{"TCP", ext}}
	if err := gw.Map(ctx, "UDP", m.ports.MediaUDP, m.ports.MediaUDP, "Windstream media"); err != nil {
		return 0, fmt.Errorf("map UDP %d: %w", m.ports.MediaUDP, err)
	}
	maps = append(maps, mapping{"UDP", m.ports.MediaUDP})
	if m.ports.MediaTCP != 0 {
		if err := gw.Map(ctx, "TCP", m.ports.MediaTCP, m.ports.MediaTCP, "Windstream media (TCP)"); err == nil {
			maps = append(maps, mapping{"TCP", m.ports.MediaTCP})
		}
	}
	m.mu.Lock()
	m.mapped = maps
	m.mu.Unlock()
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

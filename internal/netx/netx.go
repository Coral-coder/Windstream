// Package netx automates reachability from the internet: UPnP port mapping on
// the home router, public IP discovery, and a DNS name for the public IP
// (sslip.io) that Let's Encrypt can issue a certificate for.
package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/pion/stun/v3"
)

// IsPublic reports whether ip is reachable from the internet (not private,
// CGNAT, loopback, link-local or unspecified).
func IsPublic(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xC0 == 64 { // 100.64.0.0/10
		return false
	}
	return true
}

// IsCGNAT reports whether ip is in the carrier-grade NAT range.
func IsCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1]&0xC0 == 64
}

// SSLIPHost returns the sslip.io name that resolves to ip, e.g.
// 203.0.113.5 -> 203-0-113-5.sslip.io.
func SSLIPHost(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return strings.ReplaceAll(v4.String(), ".", "-") + ".sslip.io"
	}
	return strings.ReplaceAll(ip.String(), ":", "-") + ".sslip.io"
}

// LANIP returns the address of the interface used for the default route.
func LANIP() net.IP {
	c, err := net.Dial("udp4", "192.0.2.1:9") // TEST-NET; no packet is sent
	if err != nil {
		return nil
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP
}

// STUNPublicIP asks a STUN server for this host's public address.
func STUNPublicIP(ctx context.Context, server string) (net.IP, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "udp4", server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Second)
	}
	_ = conn.SetDeadline(deadline)
	req := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	buf := make([]byte, 1500)
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := conn.Write(req.Raw); err != nil {
			return nil, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
		n, err := conn.Read(buf)
		if err != nil {
			continue
		}
		res := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
		if err := res.Decode(); err != nil || res.TransactionID != req.TransactionID {
			continue
		}
		var xor stun.XORMappedAddress
		if err := xor.GetFrom(res); err != nil {
			return nil, err
		}
		return xor.IP, nil
	}
	return nil, errors.New("no STUN response")
}

// igd is the subset of the UPnP WANIPConnection/WANPPPConnection services we use.
type igd interface {
	AddPortMappingCtx(ctx context.Context, remoteHost string, extPort uint16, proto string, intPort uint16,
		intClient string, enabled bool, desc string, lease uint32) error
	DeletePortMappingCtx(ctx context.Context, remoteHost string, extPort uint16, proto string) error
	GetExternalIPAddressCtx(ctx context.Context) (string, error)
	LocalAddr() net.IP
}

// Gateway is a UPnP Internet Gateway Device (the home router).
type Gateway struct {
	c    igd
	Kind string
}

// DiscoverGateway finds the router's UPnP port-mapping service.
func DiscoverGateway(ctx context.Context) (*Gateway, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	type result struct {
		c    igd
		kind string
	}
	ch := make(chan result, 3)
	go func() {
		cs, _, _ := internetgateway2.NewWANIPConnection2ClientsCtx(ctx)
		if len(cs) > 0 {
			ch <- result{cs[0], "WANIPConnection:2"}
			return
		}
		ch <- result{}
	}()
	go func() {
		cs, _, _ := internetgateway2.NewWANIPConnection1ClientsCtx(ctx)
		if len(cs) > 0 {
			ch <- result{cs[0], "WANIPConnection:1"}
			return
		}
		ch <- result{}
	}()
	go func() {
		cs, _, _ := internetgateway2.NewWANPPPConnection1ClientsCtx(ctx)
		if len(cs) > 0 {
			ch <- result{cs[0], "WANPPPConnection:1"}
			return
		}
		ch <- result{}
	}()
	var best result
	for i := 0; i < 3; i++ {
		r := <-ch
		if r.c != nil && (best.c == nil || r.kind < best.kind) { // prefer IP over PPP, v2 over v1 among IP
			best = r
		}
	}
	if best.c == nil {
		return nil, errors.New("no UPnP router found (UPnP may be disabled on the router)")
	}
	return &Gateway{c: best.c, Kind: best.kind}, nil
}

// ExternalIP returns the router's WAN address.
func (g *Gateway) ExternalIP(ctx context.Context) (net.IP, error) {
	s, err := g.c.GetExternalIPAddressCtx(ctx)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return nil, fmt.Errorf("router reported invalid external IP %q", s)
	}
	return ip, nil
}

// Map forwards extPort on the router to intPort on this PC. It prefers a
// renewable one-hour lease and falls back to a permanent mapping on routers
// that only support those.
func (g *Gateway) Map(ctx context.Context, proto string, extPort, intPort uint16, desc string) error {
	local := g.c.LocalAddr()
	if local == nil {
		local = LANIP()
	}
	if local == nil {
		return errors.New("cannot determine this PC's LAN address")
	}
	err := g.c.AddPortMappingCtx(ctx, "", extPort, proto, intPort, local.String(), true, desc, 3600)
	if err != nil && (strings.Contains(err.Error(), "725") || strings.Contains(err.Error(), "OnlyPermanentLeases")) {
		err = g.c.AddPortMappingCtx(ctx, "", extPort, proto, intPort, local.String(), true, desc, 0)
	}
	return err
}

// Unmap removes a mapping.
func (g *Gateway) Unmap(ctx context.Context, proto string, extPort uint16) error {
	return g.c.DeletePortMappingCtx(ctx, "", extPort, proto)
}

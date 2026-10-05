package netx

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

func TestClassification(t *testing.T) {
	for ip, want := range map[string]bool{
		"203.0.113.5": true, "8.8.8.8": true, "192.168.1.2": false, "10.0.0.1": false,
		"100.64.1.1": false, "100.127.255.255": false, "100.128.0.1": true, "127.0.0.1": false, "169.254.1.1": false,
	} {
		if got := IsPublic(net.ParseIP(ip)); got != want {
			t.Errorf("IsPublic(%s) = %v", ip, got)
		}
	}
	if !IsCGNAT(net.ParseIP("100.100.1.1")) || IsCGNAT(net.ParseIP("100.128.0.1")) {
		t.Error("CGNAT range wrong")
	}
	if h := SSLIPHost(net.ParseIP("203.0.113.5")); h != "203-0-113-5.sslip.io" {
		t.Errorf("SSLIPHost = %s", h)
	}
}

// TestSTUNPublicIP runs against an in-process STUN responder.
func TestSTUNPublicIP(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			req := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
			if req.Decode() != nil {
				continue
			}
			ua := addr.(*net.UDPAddr)
			res := stun.MustBuild(stun.NewTransactionIDSetter(req.TransactionID), stun.BindingSuccess,
				&stun.XORMappedAddress{IP: net.ParseIP("198.51.100.7"), Port: ua.Port})
			pc.WriteTo(res.Raw, addr)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ip, err := STUNPublicIP(ctx, pc.LocalAddr().String())
	if err != nil || !ip.Equal(net.ParseIP("198.51.100.7")) {
		t.Fatalf("got %v %v", ip, err)
	}
}

type fakeIGD struct {
	ext       string
	maps      map[string]uint32
	only0     bool
	refuse443 bool
}

func (f *fakeIGD) AddPortMappingCtx(_ context.Context, _ string, ext uint16, proto string, _ uint16, _ string, _ bool, _ string, lease uint32) error {
	if f.only0 && lease != 0 {
		return &net.OpError{Op: "upnp", Err: errString("SOAP fault 725 OnlyPermanentLeasesSupported")}
	}
	if f.refuse443 && ext == 443 {
		return errString("718 ConflictInMappingEntry")
	}
	f.maps[proto+":"+itoa(ext)] = lease
	return nil
}
func (f *fakeIGD) DeletePortMappingCtx(_ context.Context, _ string, ext uint16, proto string) error {
	delete(f.maps, proto+":"+itoa(ext))
	return nil
}
func (f *fakeIGD) GetExternalIPAddressCtx(context.Context) (string, error) { return f.ext, nil }
func (f *fakeIGD) LocalAddr() net.IP                                       { return net.ParseIP("192.168.1.20") }

type errString string

func (e errString) Error() string { return string(e) }

func itoa(p uint16) string { return strconv.Itoa(int(p)) }

func TestMapFallbacks(t *testing.T) {
	f := &fakeIGD{ext: "203.0.113.9", maps: map[string]uint32{}, only0: true, refuse443: true}
	m := NewManager(nil, Ports{HTTPSInternal: 8443, HTTPSExternal: 443, MediaUDP: 8444}, true, "", nil)
	ext, err := m.mapAll(context.Background(), &Gateway{c: f, Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if ext != 8443 {
		t.Errorf("expected fallback to 8443, got %d", ext)
	}
	if len(f.maps) != 2 {
		t.Errorf("mappings = %v", f.maps)
	}
	for k, lease := range f.maps {
		if lease != 0 {
			t.Errorf("%s lease %d, want permanent", k, lease)
		}
	}
	m.unmapAll()
	if len(f.maps) != 0 {
		t.Errorf("mappings left after unmap: %v", f.maps)
	}
}

package netx

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeRouter is a UPnP router whose own web service holds public port 443:
// it accepts the mapping, but requests to 443 still reach the router.
type fakeRouter struct {
	mu     sync.Mutex
	mapped map[string]uint16 // "TCP 443" -> internal port
	wan    string
}

func (f *fakeRouter) AddPortMappingCtx(_ context.Context, _ string, ext uint16, proto string, in uint16, _ string, _ bool, _ string, _ uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mapped[fmt.Sprintf("%s %d", proto, ext)] = in
	return nil
}

func (f *fakeRouter) DeletePortMappingCtx(_ context.Context, _ string, ext uint16, proto string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.mapped, fmt.Sprintf("%s %d", proto, ext))
	return nil
}

func (f *fakeRouter) GetExternalIPAddressCtx(context.Context) (string, error) {
	if f.wan != "" {
		return f.wan, nil
	}
	return "81.2.69.160", nil
}
func (f *fakeRouter) GetSpecificPortMappingEntryCtx(_ context.Context, _ string, ext uint16, proto string) (uint16, string, bool, string, uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, ok := f.mapped[fmt.Sprintf("%s %d", proto, ext)]
	if !ok {
		return 0, "", false, "", 0, fmt.Errorf("<UPnPError><errorCode>714</errorCode><errorDescription>NoSuchEntryInArray</errorDescription></UPnPError>")
	}
	return in, "192.168.1.20", true, "", 3600, nil
}

func (f *fakeRouter) LocalAddr() net.IP { return net.IPv4(192, 168, 1, 20) }

func TestRouterHoldingPort443MovesTheLink(t *testing.T) {
	router := &fakeRouter{mapped: map[string]uint16{}}
	m := NewManager(nil, Ports{HTTPSInternal: 8443, HTTPSExternal: 443, MediaUDP: 8444}, true, "", nil)
	m.discover = func(context.Context) (*Gateway, error) { return &Gateway{c: router, Kind: "fake"}, nil }
	var checked []string
	m.SetVerifier(func(_ context.Context, url string) error {
		checked = append(checked, url)
		if !strings.HasSuffix(url, ":8443") {
			return &ForeignAnswerError{"another device answered instead of Windstream (HTTP 404)"}
		}
		return nil
	})
	m.Refresh(context.Background())
	st := m.Status()
	if st.PublicCheck != "ok" || st.HTTPSExternal != 8443 || st.PublicURL != "https://81-2-69-160.sslip.io:8443" {
		t.Fatalf("status = %+v (checked %v)", st, checked)
	}
	if st.Link() != st.PublicURL {
		t.Fatalf("link = %q", st.Link())
	}
	router.mu.Lock()
	_, still443 := router.mapped["TCP 443"]
	router.mu.Unlock()
	if still443 {
		t.Error("the unusable 443 mapping was left on the router")
	}
	// Later refreshes go straight to the working port.
	checked = nil
	m.Refresh(context.Background())
	if len(checked) != 1 || !strings.HasSuffix(checked[0], ":8443") {
		t.Errorf("second refresh checked %v", checked)
	}
	m.unmapAll()
	if len(router.mapped) != 0 {
		t.Errorf("mappings left behind: %v", router.mapped)
	}
}

func TestForeignAnswerEverywhereFallsBackToHomeLink(t *testing.T) {
	router := &fakeRouter{mapped: map[string]uint16{}}
	m := NewManager(nil, Ports{HTTPSInternal: 8443, HTTPSExternal: 443, MediaUDP: 8444}, true, "", nil)
	m.discover = func(context.Context) (*Gateway, error) { return &Gateway{c: router, Kind: "fake"}, nil }
	m.SetVerifier(func(context.Context, string) error {
		return &ForeignAnswerError{"another device answered instead of Windstream (HTTP 404)"}
	})
	m.Refresh(context.Background())
	st := m.Status()
	if !st.PublicCheckForeign || st.Link() != st.LANURL || len(st.Forward) == 0 {
		t.Fatalf("status = %+v, link %q", st, st.Link())
	}
}

// refusing443 is a router like the user's: it keeps 443 for itself
// (UPnP error 729) and cannot loop its public address back inside.
type refusing443 struct {
	fakeRouter
	tries443 int
}

func (f *refusing443) AddPortMappingCtx(ctx context.Context, h string, ext uint16, proto string, in uint16, c string, e bool, d string, l uint32) error {
	if ext == 443 {
		f.tries443++
		return fmt.Errorf(`SOAP fault. Code: s:Client | Explanation: UPnPError | Detail: <UPnPError><errorCode>729</errorCode><errorDescription>ConflictWithOtherMechanisms</errorDescription></UPnPError>`)
	}
	return f.fakeRouter.AddPortMappingCtx(ctx, h, ext, proto, in, c, e, d, l)
}

func TestRouterRefusing443AndNoLoopback(t *testing.T) {
	router := &refusing443{fakeRouter: fakeRouter{mapped: map[string]uint16{}, wan: "38.40.90.74"}}
	m := NewManager(nil, Ports{HTTPSInternal: 9443, HTTPSExternal: 443, MediaUDP: 8444}, true, "", nil)
	m.discover = func(context.Context) (*Gateway, error) { return &Gateway{c: router, Kind: "fake"}, nil }
	m.SetVerifier(func(context.Context, string) error {
		return &ForeignAnswerError{"another device answered instead of Windstream (HTTP 404)"}
	})
	m.Refresh(context.Background())
	m.Refresh(context.Background())
	if router.tries443 != 1 {
		t.Errorf("asked the router for 443 %d times; it refused once, that should be the end of it", router.tries443)
	}
	st := m.Status()
	if st.PublicURL != "https://38-40-90-74.sslip.io:9443" {
		t.Fatalf("public url %q", st.PublicURL)
	}
	if st.Link() != st.LANURL {
		t.Fatalf("unconfirmed link handed out: %q", st.Link())
	}
	// Someone opens it from the internet: now it is known to work.
	host := st.Hostname
	m.ConfirmFromOutside("evil.example") // another name proves nothing
	if m.Status().SeenFromOutside {
		t.Fatal("a visit under another name confirmed the link")
	}
	m.ConfirmFromOutside(strings.ToUpper(host))
	st = m.Status()
	if !st.SeenFromOutside || st.PublicCheck != "ok" || st.Link() != st.PublicURL {
		t.Fatalf("after a visit from outside: %+v link %q", st, st.Link())
	}
	// The next re-check still cannot reach it from inside, but the visit counts.
	m.Refresh(context.Background())
	if st := m.Status(); st.Link() != st.PublicURL || !st.SeenFromOutside {
		t.Fatalf("confirmation lost on refresh: %+v", st)
	}
}

// noRenew refuses to add a mapping that already exists (UPnP 606), as the
// user's router did every other refresh; when down, it refuses everything.
type noRenew struct {
	refusing443
	down bool
}

func (f *noRenew) AddPortMappingCtx(ctx context.Context, h string, ext uint16, proto string, in uint16, c string, e bool, d string, l uint32) error {
	f.mu.Lock()
	_, exists := f.mapped[fmt.Sprintf("%s %d", proto, ext)]
	down := f.down
	f.mu.Unlock()
	if exists || down {
		return fmt.Errorf("<UPnPError><errorCode>606</errorCode><errorDescription>Action not authorized</errorDescription></UPnPError>")
	}
	return f.refusing443.AddPortMappingCtx(ctx, h, ext, proto, in, c, e, d, l)
}

func TestRouterRefusingRenewals(t *testing.T) {
	router := &noRenew{refusing443: refusing443{fakeRouter: fakeRouter{mapped: map[string]uint16{}, wan: "38.40.90.74"}}}
	m := NewManager(nil, Ports{HTTPSInternal: 9443, HTTPSExternal: 443, MediaUDP: 8444}, true, "", nil)
	m.discover = func(context.Context) (*Gateway, error) { return &Gateway{c: router, Kind: "fake"}, nil }
	for i := 0; i < 3; i++ {
		m.Refresh(context.Background())
		if st := m.Status(); st.UPnP != "mapped" || st.HTTPSExternal != 9443 {
			t.Fatalf("refresh %d: %s %q, port %d", i, st.UPnP, st.UPnPDetail, st.HTTPSExternal)
		}
	}
	// The router refuses everything for a while: the link must keep the
	// port that worked, never fall back to the router's own 443.
	router.mu.Lock()
	router.down = true
	router.mu.Unlock()
	m.Refresh(context.Background())
	if st := m.Status(); st.HTTPSExternal != 9443 || strings.HasSuffix(st.PublicURL, ".io") {
		t.Fatalf("while refused: port %d, url %q", st.HTTPSExternal, st.PublicURL)
	}
	router.mu.Lock()
	_, kept := router.mapped["TCP 9443"]
	router.mu.Unlock()
	if !kept {
		t.Fatal("the working mapping was removed")
	}
	if st := m.Status(); st.UPnP != "mapped" {
		t.Fatalf("existing mapping not recognised while renewals are refused: %s %q", st.UPnP, st.UPnPDetail)
	}
}

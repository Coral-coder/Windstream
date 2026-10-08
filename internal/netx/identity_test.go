package netx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckIdentity(t *testing.T) {
	ours := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == IdentityPath {
			_, _ = w.Write([]byte("tok123"))
			return
		}
		http.NotFound(w, r)
	}))
	defer ours.Close()
	// What the user hit: the router's own page answering on the port.
	router := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404,"error":"you entered no-man zone"}`))
	}))
	defer router.Close()
	plain := httptest.NewServer(http.NotFoundHandler()) // not HTTPS at all
	defer plain.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := CheckIdentity(ctx, ours.URL+"/", "tok123"); err != nil {
		t.Fatalf("own server rejected: %v", err)
	}
	if err := CheckIdentity(ctx, ours.URL, "other"); err == nil {
		t.Fatal("another Windstream's token accepted")
	}
	if err := CheckIdentity(ctx, router.URL, "tok123"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("router page accepted or unclear: %v", err)
	}
	if err := CheckIdentity(ctx, "https"+strings.TrimPrefix(plain.URL, "http"), "tok123"); err == nil {
		t.Fatal("plain HTTP server accepted")
	}
	closed := httptest.NewTLSServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	if err := CheckIdentity(ctx, url, "tok123"); err == nil {
		t.Fatal("closed port accepted")
	}
}

func TestStatusLink(t *testing.T) {
	st := Status{PublicURL: "https://pub", LANURL: "https://lan"}
	if st.Link() != "https://pub" {
		t.Error("unchecked public link not used")
	}
	st.PublicCheck = "failed"
	if st.Link() != "https://lan" {
		t.Error("a public link that reaches something else was handed out")
	}
	st.UPnP = "mapped" // likely only the router not looping back
	if st.Link() != "https://pub" {
		t.Error("mapped public link not used")
	}
	if (Status{LANURL: "https://lan"}).Link() != "https://lan" {
		t.Error("no LAN fallback")
	}
}

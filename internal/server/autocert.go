package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"

	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/coral-coder/windstream/internal/safe"
)

// AutoTLS serves a Let's Encrypt certificate for the current public hostname
// (obtained with the TLS-ALPN-01 challenge, so only the HTTPS port needs to
// be reachable) and a persistent self-signed certificate for everything else
// (LAN IP, localhost) or while the public certificate is unavailable.
type AutoTLS struct {
	log  *slog.Logger
	dir  string
	acme *autocert.Manager

	mu       sync.RWMutex
	host     string
	acmeOn   bool // the public HTTPS port is 443, so Let's Encrypt can validate
	haveCert bool // a public certificate was obtained (it is cached)
	self     *tls.Certificate
	lastFail time.Time
}

// acmeRetry is how long to serve the self-signed certificate after a failed
// attempt before asking Let's Encrypt again: every handshake would otherwise
// retry, and repeated failures get the address rate limited.
const acmeRetry = 10 * time.Minute

// NewAutoTLS stores ACME account/certificates and the self-signed pair in dir.
func NewAutoTLS(dir, email string, log *slog.Logger) (*AutoTLS, error) {
	a := &AutoTLS{log: log, dir: dir}
	a.acme = &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Cache:  autocert.DirCache(filepath.Join(dir, "acme")),
		Email:  email,
		HostPolicy: func(_ context.Context, host string) error {
			if h := a.Host(); h != "" && strings.EqualFold(host, h) {
				return nil
			}
			return fmt.Errorf("host %q not allowed", host)
		},
	}
	if err := a.loadSelfSigned(); err != nil {
		return nil, err
	}
	return a, nil
}

// SetHost changes the public hostname (when the public IP changes).
func (a *AutoTLS) SetHost(host string) {
	a.mu.Lock()
	if h := strings.ToLower(host); h != a.host {
		a.host, a.haveCert, a.lastFail = h, false, time.Time{}
	}
	a.mu.Unlock()
}

// SetACME enables or disables obtaining a public certificate.
func (a *AutoTLS) SetACME(on bool) {
	a.mu.Lock()
	a.acmeOn = on
	a.mu.Unlock()
}

// tryACME reports whether a handshake for the public host should go to
// Let's Encrypt (or its cache).
func (a *AutoTLS) tryACME(hello *tls.ClientHelloInfo) bool {
	for _, p := range hello.SupportedProtos {
		if p == "acme-tls/1" {
			return true // a validation request from Let's Encrypt itself
		}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.haveCert || (a.acmeOn && time.Since(a.lastFail) > acmeRetry)
}

func (a *AutoTLS) noteResult(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.haveCert = true
		return
	}
	if time.Since(a.lastFail) > acmeRetry {
		a.log.Warn("public certificate unavailable; serving self-signed (is the HTTPS port reachable from the internet on 443?)", "host", a.host, "error", err)
	}
	a.lastFail = time.Now()
}

// Host returns the public hostname.
func (a *AutoTLS) Host() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.host
}

// GetCertificate implements tls.Config.GetCertificate.
func (a *AutoTLS) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := a.Host()
	if host != "" && strings.EqualFold(hello.ServerName, host) && a.tryACME(hello) {
		cert, err := a.acme.GetCertificate(hello)
		// Do not fall back during the ACME challenge itself.
		for _, p := range hello.SupportedProtos {
			if p == "acme-tls/1" {
				return cert, err
			}
		}
		a.noteResult(err)
		if err == nil {
			return cert, nil
		}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.self, nil
}

// Warm requests the public certificate in the background so the first
// visitor does not wait for issuance.
func (a *AutoTLS) Warm(ctx context.Context) {
	host := a.Host()
	a.mu.RLock()
	on := a.acmeOn
	a.mu.RUnlock()
	if host == "" || !on {
		return
	}
	go func() {
		defer safe.Recover(a.log, "certificate warm-up")
		// Issuance needs the router mapping to be live; retry a few times.
		for i := 0; i < 3 && ctx.Err() == nil; i++ {
			_, err := a.acme.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
			if a.Host() != host {
				return // the address changed meanwhile
			}
			a.noteResult(err)
			if err == nil {
				a.log.Info("public HTTPS certificate ready", "host", host)
				return
			} else if i == 2 {
				a.log.Warn("could not obtain public certificate", "host", host, "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(60*(i+1)) * time.Second):
			}
		}
	}()
}

// SelfSignedFingerprint returns the SHA-256 fingerprint of the LAN certificate.
func (a *AutoTLS) SelfSignedFingerprint() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return Fingerprint(a.self)
}

// loadSelfSigned reuses the stored self-signed certificate when it still
// covers this machine's addresses, so browsers that accepted it keep doing so.
func (a *AutoTLS) loadSelfSigned() error {
	certPath, keyPath := filepath.Join(a.dir, "lan-cert.pem"), filepath.Join(a.dir, "lan-key.pem")
	hosts := localNames()
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil &&
			time.Until(leaf.NotAfter) > 30*24*time.Hour && covers(leaf, hosts) {
			a.self = &cert
			return nil
		}
	}
	certPEM, keyPEM, err := GenerateSelfSigned(hosts, 825*24*time.Hour)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	a.self = &cert
	return nil
}

func localNames() []string {
	names := []string{"localhost", "127.0.0.1", "::1"}
	if hn, err := os.Hostname(); err == nil {
		names = append(names, hn)
	}
	addrs, _ := net.InterfaceAddrs()
	for _, ad := range addrs {
		if ipn, ok := ad.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
			names = append(names, ipn.IP.String())
		}
	}
	return names
}

func covers(leaf *x509.Certificate, names []string) bool {
	for _, n := range names {
		if leaf.VerifyHostname(n) != nil {
			return false
		}
	}
	return true
}

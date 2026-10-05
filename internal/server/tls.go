package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"sync"
	"time"
)

// baseTLSConfig returns a modern TLS configuration: TLS 1.2 minimum with
// AEAD-only suites, TLS 1.3 preferred.
func baseTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:       tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		},
		NextProtos: []string{"h2", "http/1.1"},
	}
}

// certReloader serves a certificate from disk and picks up renewals (e.g.
// from certbot) without a restart by re-reading when the files change.
type certReloader struct {
	certFile, keyFile string
	mu                sync.Mutex
	cert              *tls.Certificate
	modTime           time.Time
	lastCheck         time.Time
}

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := r.reload(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) reload() error {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load tls key pair: %w", err)
	}
	st, err := os.Stat(r.certFile)
	if err != nil {
		return err
	}
	r.cert = &cert
	r.modTime = st.ModTime()
	r.lastCheck = time.Now()
	return nil
}

func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.lastCheck) > time.Minute {
		r.lastCheck = time.Now()
		if st, err := os.Stat(r.certFile); err == nil && st.ModTime().After(r.modTime) {
			if err := r.reload(); err != nil {
				return r.cert, nil // keep serving the old cert; log is noisy here
			}
		}
	}
	return r.cert, nil
}

// GenerateSelfSigned creates an ECDSA P-256 certificate for hosts. It is used
// for `windstream gen-cert` and for the ephemeral tls.self_signed mode.
func GenerateSelfSigned(hosts []string, validFor time.Duration) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Windstream", Organization: []string{"Windstream self-signed"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// Fingerprint returns the SHA-256 fingerprint of a certificate for display.
func Fingerprint(cert *tls.Certificate) string {
	if cert == nil || len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	h := hex.EncodeToString(sum[:])
	out := make([]byte, 0, len(h)+len(h)/2)
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, h[i], h[i+1])
	}
	return string(out)
}

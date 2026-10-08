package netx

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// IdentityPath is where the HTTPS server answers with its identity token,
// so the app can tell its own server from whatever else answers at an
// address (usually the router's own web page when a port is not forwarded).
const IdentityPath = "/.well-known/windstream-id"

// CheckIdentity reports whether baseURL reaches the server holding token.
// Certificates are not verified (the public one may not be issued yet);
// only the token is sent back, never anything secret.
func CheckIdentity(ctx context.Context, baseURL, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+IdentityPath, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // identity is checked by token
			DisableKeepAlives: true,
			Proxy:             nil,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		var ne net.Error
		var tlsErr *tls.RecordHeaderError
		switch {
		case errors.As(err, &tlsErr):
			return errors.New("another device answered at this address (it does not speak HTTPS on this port); the port is not forwarded to this PC")
		case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded):
			return errors.New("nothing answered: the port is not forwarded to this PC, or the router cannot reach its own public address from inside your home")
		default:
			return fmt.Errorf("could not connect (%v): the port is not forwarded to this PC, or the router cannot reach its own public address from inside your home", trimNetErr(err))
		}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode == http.StatusOK && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(string(body))), []byte(token)) == 1 {
		return nil
	}
	return fmt.Errorf("another device answered instead of Windstream (HTTP %d), usually the router's own web page: the port is not forwarded to this PC", resp.StatusCode)
}

func trimNetErr(err error) string {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Err.Error()
	}
	return err.Error()
}

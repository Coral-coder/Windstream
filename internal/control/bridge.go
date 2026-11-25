package control

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"

	"github.com/coral-coder/windstream/web"
)

// PanelHost is implemented by platforms that show the control panel in a
// window of their own instead of a page served on a local port. The
// controller then opens no dashboard port at all; the window reaches the
// dashboard API through the bridge.
type PanelHost interface {
	HostPanel(b *PanelBridge)
}

// PanelBridge runs dashboard API calls in-process, for a control window.
// It holds the window's sign-in like a browser would hold the cookie.
type PanelBridge struct {
	h    http.Handler
	host string
	log  *slog.Logger

	mu     sync.Mutex
	cookie string
}

// BridgeRequest is one API call from the window.
type BridgeRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   string `json:"body,omitempty"`
}

// BridgeResponse answers a BridgeRequest. Body is base64 when Base64 is
// set (downloads such as the logs zip).
type BridgeResponse struct {
	ID          int    `json:"id"`
	Status      int    `json:"status"`
	Body        string `json:"body"`
	Base64      bool   `json:"base64,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Filename    string `json:"filename,omitempty"`
}

func newPanelBridge(p *panel) *PanelBridge {
	host := ""
	for h := range p.hosts {
		if strings.HasPrefix(h, "127.0.0.1:") {
			host = h
		}
	}
	return &PanelBridge{h: p.routes(), host: host, log: p.c.log}
}

// Log is the controller's logger, for the window's own messages.
func (b *PanelBridge) Log() *slog.Logger { return b.log }

var dispositionName = regexp.MustCompile(`filename="?([^";]+)"?`)

// Call runs one API request.
func (b *PanelBridge) Call(req BridgeRequest) BridgeResponse {
	resp := BridgeResponse{ID: req.ID}
	if !strings.HasPrefix(req.Path, "/api/") || (req.Method != http.MethodGet && req.Method != http.MethodPost) {
		resp.Status, resp.Body = http.StatusNotFound, `{"error":"not found"}`
		return resp
	}
	r := httptest.NewRequest(req.Method, "http://"+b.host+req.Path, strings.NewReader(req.Body))
	r.RemoteAddr = "127.0.0.1:1"
	if req.Method == http.MethodPost {
		r.Header.Set("Origin", "http://"+b.host)
		r.Header.Set("Content-Type", "application/json")
	}
	b.mu.Lock()
	if b.cookie != "" {
		r.AddCookie(&http.Cookie{Name: panelCookie, Value: b.cookie})
	}
	b.mu.Unlock()
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, r)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == panelCookie {
			b.mu.Lock()
			b.cookie = ck.Value
			if ck.MaxAge < 0 {
				b.cookie = ""
			}
			b.mu.Unlock()
		}
	}
	resp.Status = rec.Code
	resp.ContentType = rec.Header().Get("Content-Type")
	if strings.HasPrefix(resp.ContentType, "application/json") || strings.HasPrefix(resp.ContentType, "text/") {
		resp.Body = rec.Body.String()
	} else {
		resp.Body, resp.Base64 = base64.StdEncoding.EncodeToString(rec.Body.Bytes()), true
		if m := dispositionName.FindStringSubmatch(rec.Header().Get("Content-Disposition")); m != nil {
			resp.Filename = m[1]
		}
	}
	return resp
}

// Serve answers requests read from r (one JSON object per line) on w until
// r ends (the control window's pipe), then waits for answers in flight.
func (b *PanelBridge) Serve(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var wmu sync.Mutex
	var wg sync.WaitGroup
	defer wg.Wait()
	enc := json.NewEncoder(w)
	for sc.Scan() {
		var req BridgeRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			return fmt.Errorf("control window sent a bad request: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := b.Call(req)
			wmu.Lock()
			defer wmu.Unlock()
			_ = enc.Encode(resp)
		}()
	}
	return sc.Err()
}

// PanelHTML is the control panel as one self-contained page (styles,
// script and images inlined), for a window that shows it without a server.
func PanelHTML() (string, error) {
	read := func(name string) (string, error) {
		b, err := fs.ReadFile(web.Admin, "admin/"+name)
		return string(b), err
	}
	page, err := read("index.html")
	if err != nil {
		return "", err
	}
	css, err := read("admin.css")
	if err != nil {
		return "", err
	}
	js, err := read("admin.js")
	if err != nil {
		return "", err
	}
	for _, img := range []string{"logo.png", "favicon.png"} {
		data, err := read(img)
		if err != nil {
			return "", err
		}
		page = strings.ReplaceAll(page, `"/`+img+`"`, `"data:image/png;base64,`+base64.StdEncoding.EncodeToString([]byte(data))+`"`)
	}
	repl := func(old, new string) error {
		if !strings.Contains(page, old) {
			return fmt.Errorf("control panel page: %q not found", old)
		}
		page = strings.Replace(page, old, new, 1)
		return nil
	}
	if err := repl(`<link rel="stylesheet" href="/admin.css">`, "<style>\n"+css+"\n</style>"); err != nil {
		return "", err
	}
	// "</script" cannot appear inside an inline script.
	js = strings.ReplaceAll(js, "</script", `<\/script`)
	if err := repl(`<script src="/admin.js"></script>`, "<script>\n"+js+"\n</script>"); err != nil {
		return "", err
	}
	return page, nil
}

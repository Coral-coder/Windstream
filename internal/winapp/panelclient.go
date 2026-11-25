package winapp

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/coral-coder/windstream/internal/control"
)

// The pipe between the Windstream window and the server (see
// panelwindow_windows.go). Kept free of Windows calls so it is tested
// everywhere.

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// limitedBuilder keeps the first max bytes written (the window's errors).
type limitedBuilder struct {
	mu  sync.Mutex
	b   *strings.Builder
	max int
}

func (l *limitedBuilder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if room := l.max - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// bridgeClient is the window's side of the pipe.
type bridgeClient struct {
	w  *json.Encoder
	wm sync.Mutex

	mu      sync.Mutex
	next    int
	pending map[int]chan control.BridgeResponse
}

type serverMessage struct {
	Cmd string `json:"cmd,omitempty"`
	control.BridgeResponse
}

func (c *bridgeClient) call(method, path, body string) (control.BridgeResponse, error) {
	ch := make(chan control.BridgeResponse, 1)
	c.mu.Lock()
	c.next++
	id := c.next
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	c.wm.Lock()
	err := c.w.Encode(control.BridgeRequest{ID: id, Method: method, Path: path, Body: body})
	c.wm.Unlock()
	if err != nil {
		return control.BridgeResponse{}, errors.New("Windstream has stopped")
	}
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(30 * time.Second):
		return control.BridgeResponse{}, errors.New("Windstream is not answering")
	}
}

// readLoop delivers answers and runs commands until the server goes away.
func (c *bridgeClient) readLoop(r io.Reader, onCmd func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20) // the logs zip comes as one line
	for sc.Scan() {
		var m serverMessage
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Cmd != "" {
			onCmd(m.Cmd)
			continue
		}
		c.mu.Lock()
		ch := c.pending[m.ID]
		c.mu.Unlock()
		if ch != nil {
			ch <- m.BridgeResponse
		}
	}
}

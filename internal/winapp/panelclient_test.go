package winapp

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/coral-coder/windstream/internal/control"
)

// The window's client against a server answering out of order, plus a
// command interleaved with the answers.
func TestBridgeClientPipe(t *testing.T) {
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	c := &bridgeClient{w: json.NewEncoder(fromClient), pending: map[int]chan control.BridgeResponse{}}
	cmds := make(chan string, 1)
	go c.readLoop(toClient, func(cmd string) { cmds <- cmd })
	go func() {
		dec := json.NewDecoder(toServer)
		var reqs []control.BridgeRequest
		for len(reqs) < 2 {
			var r control.BridgeRequest
			if dec.Decode(&r) != nil {
				return
			}
			reqs = append(reqs, r)
		}
		enc := json.NewEncoder(fromServer)
		_ = enc.Encode(map[string]string{"cmd": "focus"})
		for i := len(reqs) - 1; i >= 0; i-- { // newest first
			_ = enc.Encode(control.BridgeResponse{ID: reqs[i].ID, Status: 200, Body: reqs[i].Path})
		}
	}()
	type result struct {
		r   control.BridgeResponse
		err error
	}
	res := make(chan result, 2)
	for _, path := range []string{"/api/one", "/api/two"} {
		go func() {
			r, err := c.call("GET", path, "")
			res <- result{r, err}
		}()
		time.Sleep(20 * time.Millisecond)
	}
	for i := 0; i < 2; i++ {
		got := <-res
		if got.err != nil || got.r.Status != 200 || (got.r.Body != "/api/one" && got.r.Body != "/api/two") {
			t.Fatalf("answer %+v %v", got.r, got.err)
		}
	}
	select {
	case cmd := <-cmds:
		if cmd != "focus" {
			t.Errorf("command %q", cmd)
		}
	case <-time.After(time.Second):
		t.Error("focus command lost")
	}
}

package stream

import (
	"testing"
	"time"

	"github.com/coral-coder/windstream/internal/protocol"
)

func TestControllerHeartbeatIsNotActivity(t *testing.T) {
	h := &Hub{}
	c := &Client{hub: h}
	old := time.Now().Add(-10 * time.Minute).UnixNano()
	idle := func() bool { return h.InputIdle() > 5*time.Minute }

	h.lastActivity.Store(old)
	pad := protocol.Message{Type: protocol.MsgGamepadState, Index: 0, Gamepad: protocol.GamepadState{Axes: [4]int16{120, 0, 0, 0}}}
	c.noteActivity(pad) // first state from this controller
	if idle() {
		t.Fatal("a controller starting to play is not activity")
	}
	h.lastActivity.Store(old)
	c.noteActivity(pad) // the browser repeating it
	c.noteActivity(protocol.Message{Type: protocol.MsgPing})
	if !idle() {
		t.Fatal("heartbeats and pings count as playing")
	}
	pad.Gamepad.Buttons = 1
	c.noteActivity(pad)
	if idle() {
		t.Fatal("a button press is not activity")
	}
	h.lastActivity.Store(old)
	c.noteActivity(protocol.Message{Type: protocol.MsgMouseMove, DX: 1})
	if idle() {
		t.Fatal("mouse movement is not activity")
	}
}

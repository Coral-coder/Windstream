package protocol

import "testing"

func TestGamepadRoundTrip(t *testing.T) {
	in := GamepadState{Buttons: 1<<BtnA | 1<<BtnGuide, Axes: [4]int16{-32768, 32767, -1, 1}, Triggers: [2]uint8{0, 255}}
	m, err := Decode(EncodeGamepadState(2, in))
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != MsgGamepadState || m.Index != 2 || m.Gamepad != in {
		t.Fatalf("decoded %+v", m)
	}
	if !m.Gamepad.Pressed(BtnGuide) || m.Gamepad.Pressed(BtnB) {
		t.Fatal("button bits wrong")
	}
}

func TestKeyAndMouse(t *testing.T) {
	m, err := Decode(append([]byte{MsgKey, 1}, "KeyW"...))
	if err != nil || m.Key != "KeyW" || !m.Down {
		t.Fatalf("key: %+v %v", m, err)
	}
	m, err = Decode([]byte{MsgMouseMove, 0xff, 0xff, 0x05, 0x00})
	if err != nil || m.DX != -1 || m.DY != 5 {
		t.Fatalf("mouse: %+v %v", m, err)
	}
	m, err = Decode([]byte{MsgMouseButton, 2, 0})
	if err != nil || m.Button != 2 || m.Down {
		t.Fatalf("button: %+v %v", m, err)
	}
	m, err = Decode([]byte{MsgPing, 0x78, 0x56, 0x34, 0x12})
	if err != nil || m.Ping != 0x12345678 {
		t.Fatalf("ping: %+v %v", m, err)
	}
	if pong := EncodePong(0x12345678); pong[0] != MsgPong || pong[1] != 0x78 {
		t.Fatalf("pong: %x", pong)
	}
}

func TestRejects(t *testing.T) {
	if _, err := Decode(nil); err != ErrShort {
		t.Fatal("empty accepted")
	}
	if _, err := Decode([]byte{0x99}); err != ErrUnknown {
		t.Fatal("unknown accepted")
	}
	if _, err := Decode([]byte{MsgGamepadState, 0}); err != ErrShort {
		t.Fatal("short gamepad accepted")
	}
	if _, err := Decode(make([]byte, MaxMessageSize+1)); err == nil {
		t.Fatal("oversize accepted")
	}
	m, _ := Decode(append([]byte{MsgGamepadConnect, 0}, "bad\x00\x01name"...))
	if m.Name != "badname" {
		t.Fatalf("sanitize: %q", m.Name)
	}
}

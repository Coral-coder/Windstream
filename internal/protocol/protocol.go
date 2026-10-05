// Package protocol defines the compact binary input protocol carried over the
// WebRTC data channels. All integers are little-endian.
//
//	0x01 GamepadState      idx u8, buttons u32, axes 4×i16, triggers 2×u8   (16 bytes)
//	0x02 GamepadConnect    idx u8, name utf8
//	0x03 GamepadDisconnect idx u8
//	0x10 Key               down u8, code utf8 (KeyboardEvent.code)
//	0x20 MouseMove         dx i16, dy i16 (relative)
//	0x21 MouseButton       button u8, down u8
//	0x22 MouseWheel        dx i16, dy i16 (notches)
//	0x30 Ping              ts u32           -> server replies 0x31 Pong ts u32
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Message type identifiers.
const (
	MsgGamepadState      byte = 0x01
	MsgGamepadConnect    byte = 0x02
	MsgGamepadDisconnect byte = 0x03
	MsgKey               byte = 0x10
	MsgMouseMove         byte = 0x20
	MsgMouseButton       byte = 0x21
	MsgMouseWheel        byte = 0x22
	MsgPing              byte = 0x30
	MsgPong              byte = 0x31
)

// MaxMessageSize bounds any single input message.
const MaxMessageSize = 256

// Standard Gamepad button indices (W3C "standard" mapping).
const (
	BtnA = iota
	BtnB
	BtnX
	BtnY
	BtnLB
	BtnRB
	BtnLT
	BtnRT
	BtnBack
	BtnStart
	BtnLS
	BtnRS
	BtnDUp
	BtnDDown
	BtnDLeft
	BtnDRight
	BtnGuide
	ButtonCount
)

// GamepadState is a full snapshot of one controller.
type GamepadState struct {
	Buttons  uint32   // bit i = standard button i pressed
	Axes     [4]int16 // LX, LY, RX, RY in -32768..32767, up/left negative
	Triggers [2]uint8 // LT, RT in 0..255
}

// Pressed reports whether standard button i is held.
func (g GamepadState) Pressed(i int) bool { return g.Buttons&(1<<uint(i)) != 0 }

// Message is a decoded input message.
type Message struct {
	Type    byte
	Index   uint8        // gamepad index for gamepad messages
	Gamepad GamepadState // for MsgGamepadState
	Name    string       // for MsgGamepadConnect
	Key     string       // for MsgKey
	Down    bool         // for MsgKey / MsgMouseButton
	Button  uint8        // for MsgMouseButton
	DX, DY  int16        // for MsgMouseMove / MsgMouseWheel
	Ping    uint32       // for MsgPing
}

var (
	// ErrShort is returned for truncated messages.
	ErrShort = errors.New("protocol: short message")
	// ErrUnknown is returned for unknown message types.
	ErrUnknown = errors.New("protocol: unknown message type")
)

// Decode parses one message.
func Decode(b []byte) (Message, error) {
	if len(b) == 0 {
		return Message{}, ErrShort
	}
	if len(b) > MaxMessageSize {
		return Message{}, fmt.Errorf("protocol: message too large (%d bytes)", len(b))
	}
	m := Message{Type: b[0]}
	switch b[0] {
	case MsgGamepadState:
		if len(b) < 16 {
			return m, ErrShort
		}
		m.Index = b[1]
		m.Gamepad.Buttons = binary.LittleEndian.Uint32(b[2:])
		for i := 0; i < 4; i++ {
			m.Gamepad.Axes[i] = int16(binary.LittleEndian.Uint16(b[6+2*i:]))
		}
		m.Gamepad.Triggers[0] = b[14]
		m.Gamepad.Triggers[1] = b[15]
	case MsgGamepadConnect:
		if len(b) < 2 {
			return m, ErrShort
		}
		m.Index = b[1]
		m.Name = sanitize(b[2:])
	case MsgGamepadDisconnect:
		if len(b) < 2 {
			return m, ErrShort
		}
		m.Index = b[1]
	case MsgKey:
		if len(b) < 3 {
			return m, ErrShort
		}
		m.Down = b[1] != 0
		m.Key = sanitize(b[2:])
	case MsgMouseMove, MsgMouseWheel:
		if len(b) < 5 {
			return m, ErrShort
		}
		m.DX = int16(binary.LittleEndian.Uint16(b[1:]))
		m.DY = int16(binary.LittleEndian.Uint16(b[3:]))
	case MsgMouseButton:
		if len(b) < 3 {
			return m, ErrShort
		}
		m.Button = b[1]
		m.Down = b[2] != 0
	case MsgPing:
		if len(b) < 5 {
			return m, ErrShort
		}
		m.Ping = binary.LittleEndian.Uint32(b[1:])
	default:
		return m, ErrUnknown
	}
	return m, nil
}

// EncodePong builds the reply to a ping.
func EncodePong(ts uint32) []byte {
	out := make([]byte, 5)
	out[0] = MsgPong
	binary.LittleEndian.PutUint32(out[1:], ts)
	return out
}

// EncodeGamepadState serialises a state message (used by tests and tools).
func EncodeGamepadState(idx uint8, g GamepadState) []byte {
	out := make([]byte, 16)
	out[0] = MsgGamepadState
	out[1] = idx
	binary.LittleEndian.PutUint32(out[2:], g.Buttons)
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint16(out[6+2*i:], uint16(g.Axes[i]))
	}
	out[14] = g.Triggers[0]
	out[15] = g.Triggers[1]
	return out
}

func sanitize(b []byte) string {
	if !utf8.Valid(b) {
		return ""
	}
	out := make([]rune, 0, len(b))
	for _, r := range string(b) {
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

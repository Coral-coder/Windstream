package media

import (
	"testing"

	"github.com/pion/rtp"
)

func TestIsKeyframeStart(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{"single IDR", []byte{0x65, 0x88}, true},
		{"single non-IDR", []byte{0x41, 0x9a}, false},
		{"SPS", []byte{0x67, 0x42}, true},
		{"STAP-A with SPS+PPS", []byte{0x78, 0, 2, 0x67, 0x42, 0, 2, 0x68, 0xce}, true},
		{"STAP-A with SEI only", []byte{0x78, 0, 2, 0x06, 0x05}, false},
		{"FU-A IDR start", []byte{0x7c, 0x85, 0x88}, true},
		{"FU-A IDR middle", []byte{0x7c, 0x05, 0x88}, false},
		{"FU-A non-IDR start", []byte{0x7c, 0x81, 0x9a}, false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		if got := IsKeyframeStart(&rtp.Packet{Payload: c.payload}); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

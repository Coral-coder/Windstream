package media

import (
	"testing"

	"github.com/pion/rtp"
)

func TestIsKeyframeStart(t *testing.T) {
	cases := []struct {
		name    string
		codec   string
		payload []byte
		want    bool
	}{
		{"single IDR", CodecH264, []byte{0x65, 0x88}, true},
		{"single non-IDR", CodecH264, []byte{0x41, 0x9a}, false},
		{"SPS", CodecH264, []byte{0x67, 0x42}, true},
		{"STAP-A with SPS+PPS", CodecH264, []byte{0x78, 0, 2, 0x67, 0x42, 0, 2, 0x68, 0xce}, true},
		{"STAP-A with SEI only", CodecH264, []byte{0x78, 0, 2, 0x06, 0x05}, false},
		{"FU-A IDR start", CodecH264, []byte{0x7c, 0x85, 0x88}, true},
		{"FU-A IDR middle", CodecH264, []byte{0x7c, 0x05, 0x88}, false},
		{"FU-A non-IDR start", CodecH264, []byte{0x7c, 0x81, 0x9a}, false},
		{"empty", CodecH264, nil, false},

		{"hevc VPS", CodecH265, []byte{32 << 1, 1, 0x0c}, true},
		{"hevc IDR_W_RADL", CodecH265, []byte{19 << 1, 1, 0xaf}, true},
		{"hevc TRAIL_R", CodecH265, []byte{1 << 1, 1, 0xd0}, false},
		{"hevc AP with VPS", CodecH265, []byte{48 << 1, 1, 0, 2, 32 << 1, 1, 0, 2, 34 << 1, 1}, true},
		{"hevc AP with SEI", CodecH265, []byte{48 << 1, 1, 0, 2, 39 << 1, 1}, false},
		{"hevc FU IDR start", CodecH265, []byte{49 << 1, 1, 0x80 | 19, 0xaf}, true},
		{"hevc FU IDR middle", CodecH265, []byte{49 << 1, 1, 19, 0xaf}, false},
		{"hevc FU trail start", CodecH265, []byte{49 << 1, 1, 0x80 | 1, 0xaf}, false},
		{"hevc empty", CodecH265, nil, false},

		{"av1 new sequence", CodecAV1, []byte{0x18, 0x0a}, true},
		{"av1 inter frame", CodecAV1, []byte{0x10, 0x32}, false},
		{"av1 empty", CodecAV1, nil, false},
	}
	for _, c := range cases {
		if got := IsKeyframeStart(c.codec, &rtp.Packet{Payload: c.payload}); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

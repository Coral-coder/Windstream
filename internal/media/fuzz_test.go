package media

import (
	"bufio"
	"bytes"
	"testing"

	"github.com/pion/rtp"
)

func FuzzKeyframe(f *testing.F) {
	f.Add([]byte{0x78, 0, 2, 0x67, 0x42})
	f.Add([]byte{48 << 1, 1, 0, 2, 32 << 1, 1})
	f.Add([]byte{0x18})
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, c := range Codecs {
			IsKeyframeStart(c, &rtp.Packet{Payload: b})
		}
	})
}

func FuzzOgg(f *testing.F) {
	f.Add([]byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x03abc"))
	f.Fuzz(func(t *testing.T, b []byte) {
		r := NewOggReader(bufio.NewReader(bytes.NewReader(b)))
		for i := 0; i < 64; i++ {
			if _, err := r.NextPacket(); err != nil {
				return
			}
		}
	})
}

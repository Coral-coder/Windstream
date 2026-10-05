package media

import (
	"bufio"
	"bytes"
	"io"
	"testing"
	"time"
)

func TestOggReaderPackets(t *testing.T) {
	big := bytes.Repeat([]byte{0xab}, 600) // spans three lacing values
	stream := append(oggPage(1, 0, []byte("OpusHead"), []byte("OpusTags")), oggPage(1, 1, []byte{0x78, 1, 2}, big)...)
	r := NewOggReader(bufio.NewReader(bytes.NewReader(stream)))
	want := [][]byte{[]byte("OpusHead"), []byte("OpusTags"), {0x78, 1, 2}, big}
	for i, w := range want {
		got, err := r.NextPacket()
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if !bytes.Equal(got, w) {
			t.Fatalf("packet %d mismatch (len %d vs %d)", i, len(got), len(w))
		}
	}
	if _, err := r.NextPacket(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestOpusPacketDuration(t *testing.T) {
	cases := map[byte]time.Duration{
		0x78: 20 * time.Millisecond, // CELT FB 20ms, 1 frame
		0x70: 10 * time.Millisecond, // CELT FB 10ms
		0x08: 20 * time.Millisecond, // SILK NB 20ms
		0x79: 40 * time.Millisecond, // CELT FB 20ms, 2 frames
		0x60: 10 * time.Millisecond, // hybrid FB 10ms
	}
	for toc, want := range cases {
		if got := OpusPacketDuration([]byte{toc, 0}); got != want {
			t.Errorf("toc %#x: %v want %v", toc, got, want)
		}
	}
}

package stream

import (
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestRTPRewriterContinuity(t *testing.T) {
	r := newRTPRewriter()
	var out []*rtp.Packet
	run := func(seq uint16, ts uint32, n int) {
		r.restart()
		for i := 0; i < n; i++ {
			p := &rtp.Packet{Header: rtp.Header{SequenceNumber: seq + uint16(i), Timestamp: ts + uint32(i/2)*3000}}
			r.rewrite(p)
			out = append(out, p)
		}
	}
	run(60000, 4000000000, 10)
	time.Sleep(20 * time.Millisecond)
	run(17, 12345, 10) // a new ffmpeg run with unrelated numbering
	for i := 1; i < len(out); i++ {
		if out[i].SequenceNumber != out[i-1].SequenceNumber+1 {
			t.Fatalf("sequence gap at %d: %d -> %d", i, out[i-1].SequenceNumber, out[i].SequenceNumber)
		}
	}
	// Timestamps advance monotonically (mod 2^32) across the restart, by
	// about the 20 ms that passed.
	jump := out[10].Timestamp - out[9].Timestamp
	if jump < 20*90 || jump > 2000*90 {
		t.Fatalf("timestamp jump across restart = %d ticks", jump)
	}
	if out[11].Timestamp != out[10].Timestamp || out[12].Timestamp-out[11].Timestamp != 3000 {
		t.Fatalf("within-run timestamps not preserved: %d %d %d", out[10].Timestamp, out[11].Timestamp, out[12].Timestamp)
	}
}

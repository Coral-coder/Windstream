package stream

import (
	"crypto/rand"
	"encoding/binary"
	"time"

	"github.com/pion/rtp"
)

// rtpRewriter makes the outgoing video RTP stream continuous across encoder
// restarts (codec switches, encoder fallback, recovery). Every ffmpeg run
// starts at a random sequence number and timestamp; forwarding those as-is
// to a receiver that already saw the old run makes SRTP replay protection
// discard the new packets (they look old) and confuses the jitter buffer,
// freezing the picture for seconds. Viewers bound to the stream see one
// sequence-number space and one clock no matter how often the encoder
// changes underneath.
type rtpRewriter struct {
	seq      uint16
	tsOffset uint32
	lastTS   uint32
	lastWall time.Time
	started  bool
	newRun   bool
}

func newRTPRewriter() *rtpRewriter {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return &rtpRewriter{seq: binary.BigEndian.Uint16(b[:]), newRun: true}
}

// restart marks the start of a new encoder run.
func (r *rtpRewriter) restart() { r.newRun = true }

// rewrite renumbers p in place.
func (r *rtpRewriter) rewrite(p *rtp.Packet) {
	now := time.Now()
	if r.newRun {
		r.newRun = false
		r.tsOffset = 0
		if r.started {
			// Continue the 90 kHz clock from where the last run stopped,
			// advanced by the wall time that passed in between.
			elapsed := uint32(now.Sub(r.lastWall).Seconds() * 90000)
			if elapsed == 0 {
				elapsed = 1
			}
			r.tsOffset = r.lastTS + elapsed - p.Timestamp
		}
	}
	r.started = true
	r.seq++
	p.SequenceNumber = r.seq
	p.Timestamp += r.tsOffset
	r.lastTS = p.Timestamp
	r.lastWall = now
}

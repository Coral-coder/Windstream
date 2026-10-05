package media

import "github.com/pion/rtp"

// H.264 NAL unit types relevant to keyframe detection.
const (
	nalIDR   = 5
	nalSPS   = 7
	nalSTAPA = 24
	nalFUA   = 28
)

// IsKeyframeStart reports whether an H.264 RTP packet starts or carries the
// start of an IDR picture (or the SPS that precedes it).
func IsKeyframeStart(p *rtp.Packet) bool {
	pl := p.Payload
	if len(pl) < 1 {
		return false
	}
	switch t := pl[0] & 0x1f; t {
	case nalIDR, nalSPS:
		return true
	case nalSTAPA:
		for i := 1; i+2 < len(pl); {
			size := int(pl[i])<<8 | int(pl[i+1])
			i += 2
			if i >= len(pl) {
				break
			}
			if nt := pl[i] & 0x1f; nt == nalIDR || nt == nalSPS {
				return true
			}
			i += size
		}
	case nalFUA:
		if len(pl) >= 2 && pl[1]&0x80 != 0 && pl[1]&0x1f == nalIDR {
			return true
		}
	}
	return false
}

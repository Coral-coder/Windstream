package media

import "github.com/pion/rtp"

// H.264 NAL unit types relevant to keyframe detection.
const (
	nalIDR   = 5
	nalSPS   = 7
	nalSTAPA = 24
	nalFUA   = 28
)

// H.265 NAL unit types (RFC 7798).
const (
	hevcIDRWRADL = 19
	hevcIDRNLP   = 20
	hevcCRA      = 21
	hevcVPS      = 32
	hevcSPS      = 33
	hevcAP       = 48
	hevcFU       = 49
)

// IsKeyframeStart reports whether an RTP packet of the given codec starts or
// carries the start of a keyframe (or the parameter sets preceding it).
func IsKeyframeStart(codec string, p *rtp.Packet) bool {
	switch codec {
	case CodecH265:
		return isH265KeyframeStart(p.Payload)
	case CodecAV1:
		// AV1 aggregation header (RFC draft "RTP Payload Format for AV1"):
		// Z|Y|W W|N|-|-|-; N=1 marks the first packet of a coded video
		// sequence, i.e. a keyframe.
		return len(p.Payload) > 0 && p.Payload[0]&0x08 != 0
	default:
		return isH264KeyframeStart(p.Payload)
	}
}

func isH264KeyframeStart(pl []byte) bool {
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

func hevcKey(t byte) bool {
	switch t {
	case hevcIDRWRADL, hevcIDRNLP, hevcCRA, hevcVPS, hevcSPS:
		return true
	}
	return false
}

func isH265KeyframeStart(pl []byte) bool {
	if len(pl) < 2 {
		return false
	}
	switch t := (pl[0] >> 1) & 0x3f; t {
	case hevcAP:
		// 2-byte payload header, then (size(2) NAL)* units.
		for i := 2; i+2 < len(pl); {
			size := int(pl[i])<<8 | int(pl[i+1])
			i += 2
			if i >= len(pl) {
				break
			}
			if hevcKey((pl[i] >> 1) & 0x3f) {
				return true
			}
			i += size
		}
	case hevcFU:
		// 2-byte payload header, then FU header S|E|FuType(6).
		if len(pl) >= 3 && pl[2]&0x80 != 0 && hevcKey(pl[2]&0x3f) {
			return true
		}
	default:
		return hevcKey(t)
	}
	return false
}

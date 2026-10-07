package stream

import (
	"strings"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// playoutDelayURI is the RTP header extension Chrome/Edge read to decide how
// long to hold frames before rendering. Signalling min=max=0 switches the
// receiver into its cloud-gaming mode: frames are decoded and rendered the
// moment they are complete instead of being smoothed through a jitter
// buffer. Browsers that do not know it simply ignore it.
const playoutDelayURI = "http://www.webrtc.org/experiments/rtp-hdrext/playout-delay"

// playoutDelayFactory builds interceptors that stamp a zero playout delay on
// every outgoing video packet.
type playoutDelayFactory struct{}

func (playoutDelayFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &playoutDelayInterceptor{}, nil
}

type playoutDelayInterceptor struct{ interceptor.NoOp }

var zeroPlayoutDelay, _ = rtp.PlayoutDelayExtension{MinDelay: 0, MaxDelay: 0}.Marshal()

func (*playoutDelayInterceptor) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	if !strings.HasPrefix(strings.ToLower(info.MimeType), "video/") {
		return writer
	}
	var id uint8
	for _, e := range info.RTPHeaderExtensions {
		if e.URI == playoutDelayURI {
			id = uint8(e.ID) //nolint:gosec // extension IDs are 1..255
		}
	}
	if id == 0 {
		return writer // the browser did not negotiate it
	}
	return interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
		// The header is shared between every viewer's binding of the
		// track; stamp a private copy.
		h := *header
		h.Extensions = append([]rtp.Extension(nil), header.Extensions...)
		if err := h.SetExtension(id, zeroPlayoutDelay); err != nil {
			return writer.Write(header, payload, attrs)
		}
		return writer.Write(&h, payload, attrs)
	})
}

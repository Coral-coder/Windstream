package media

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"time"

	"github.com/coral-coder/windstream/internal/safe"
)

// AudioConfig configures audio capture into Opus.
type AudioConfig struct {
	FFmpeg      string
	Backend     string // wasapi | dshow | test
	Device      string
	BitrateKbps int
}

// PCMFormat describes raw PCM fed to ffmpeg on stdin (wasapi backend).
type PCMFormat struct {
	Float    bool // 32-bit float; otherwise signed integer of Bits
	Bits     int
	Rate     int
	Channels int
}

// AudioSample is one Opus packet.
type AudioSample struct {
	Data     []byte
	Duration time.Duration
}

// ErrNoWASAPI is returned when WASAPI loopback is unavailable (non-Windows).
var ErrNoWASAPI = errors.New("audio: the wasapi backend requires Windows; use backend = \"test\" or \"dshow\"")

// BuildAudioArgs assembles the ffmpeg command line. pcm is required for the
// wasapi backend and describes the stream written to ffmpeg's stdin.
func BuildAudioArgs(cfg AudioConfig, pcm *PCMFormat) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostats",
		"-fflags", "nobuffer", "-flags", "low_delay"}
	switch cfg.Backend {
	case "wasapi":
		fmtName := "f32le"
		if !pcm.Float {
			fmtName = "s" + strconv.Itoa(pcm.Bits) + "le"
		}
		args = append(args, "-f", fmtName, "-ar", strconv.Itoa(pcm.Rate), "-ac", strconv.Itoa(pcm.Channels), "-i", "pipe:0")
	case "dshow":
		args = append(args, "-nostdin", "-f", "dshow", "-audio_buffer_size", "10", "-i", "audio="+cfg.Device)
	default: // test
		args = append(args, "-nostdin", "-re", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000")
	}
	return append(args,
		"-vn", "-ac", "2", "-ar", "48000",
		"-c:a", "libopus", "-b:a", strconv.Itoa(cfg.BitrateKbps)+"k",
		"-application", "lowdelay", "-frame_duration", "10", "-vbr", "off",
		"-flush_packets", "1", "-f", "ogg", "-page_duration", "10000", "pipe:1",
	)
}

// RunAudio captures audio and calls onSample for every Opus packet. It
// returns nil only on cancellation.
func RunAudio(ctx context.Context, cfg AudioConfig, log *slog.Logger, onSample func(AudioSample)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if cfg.Backend != "wasapi" {
		return runAudioFFmpeg(ctx, cfg, nil, nil, log, onSample)
	}
	// WASAPI: capture runs on its own locked OS thread (COM) and feeds PCM to
	// ffmpeg's stdin; ffmpeg only encodes.
	errCh := make(chan error, 2)
	started := make(chan struct{})
	err := runLoopback(ctx, cfg.Device, log, func(pcm PCMFormat, src io.Reader) {
		close(started)
		go func() {
			var ffErr error
			if safe.Call(log, "audio encoder", func() { ffErr = runAudioFFmpeg(ctx, cfg, &pcm, src, log, onSample) }) {
				ffErr = errors.New("audio encoder hit an internal error")
			}
			errCh <- ffErr
			cancel()
			// Unblock the capture loop if it is mid-write to a dead encoder.
			if c, ok := src.(io.Closer); ok {
				_ = c.Close()
			}
		}()
	})
	select {
	case <-started:
		if ffErr := <-errCh; ffErr != nil {
			return ffErr
		}
	default:
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func runAudioFFmpeg(ctx context.Context, cfg AudioConfig, pcm *PCMFormat, stdin io.Reader, log *slog.Logger, onSample func(AudioSample)) error {
	args := BuildAudioArgs(cfg, pcm)
	cmd := exec.CommandContext(ctx, cfg.FFmpeg, args...)
	configureCmd(cmd)
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	log.Debug("starting ffmpeg audio", "args", args)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	go logStderr(stderr, log.With("pipeline", "audio"))

	ogg := NewOggReader(bufio.NewReaderSize(stdout, 16*1024))
	var readErr error
	for {
		pkt, err := ogg.NextPacket()
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
		if len(pkt) >= 8 && (string(pkt[:8]) == "OpusHead" || string(pkt[:8]) == "OpusTags") {
			continue
		}
		onSample(AudioSample{Data: pkt, Duration: OpusPacketDuration(pkt)})
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if readErr != nil {
		return fmt.Errorf("read ffmpeg output: %w", readErr)
	}
	if waitErr != nil {
		return fmt.Errorf("ffmpeg exited: %w", waitErr)
	}
	return errors.New("ffmpeg exited unexpectedly")
}

// logStderr forwards ffmpeg's warnings to the log, at most 20 lines per 10
// seconds so a chatty encoder cannot flood the log; the rest are counted.
func logStderr(r io.Reader, log *slog.Logger) {
	defer safe.Recover(log, "ffmpeg log reader")
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	window, logged, dropped := time.Now(), 0, 0
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if time.Since(window) > 10*time.Second {
			if dropped > 0 {
				log.Warn("ffmpeg: further messages suppressed", "count", dropped)
			}
			window, logged, dropped = time.Now(), 0, 0
		}
		if logged >= 20 {
			dropped++
			continue
		}
		logged++
		log.Warn("ffmpeg: " + line)
	}
	// Drain anything left so ffmpeg never blocks writing to a full pipe.
	_, _ = io.Copy(io.Discard, r)
}

// OggReader extracts logical packets from an Ogg bitstream, handling packets
// that span pages.
type OggReader struct {
	r       *bufio.Reader
	partial []byte
	queue   [][]byte
}

// NewOggReader wraps r.
func NewOggReader(r *bufio.Reader) *OggReader { return &OggReader{r: r} }

// NextPacket returns the next complete packet.
func (o *OggReader) NextPacket() ([]byte, error) {
	for len(o.queue) == 0 {
		if err := o.readPage(); err != nil {
			return nil, err
		}
	}
	pkt := o.queue[0]
	o.queue = o.queue[1:]
	return pkt, nil
}

func (o *OggReader) readPage() error {
	var hdr [27]byte
	if _, err := io.ReadFull(o.r, hdr[:]); err != nil {
		return err
	}
	if string(hdr[:4]) != "OggS" {
		return errors.New("ogg: lost sync")
	}
	lacing := make([]byte, int(hdr[26]))
	if _, err := io.ReadFull(o.r, lacing); err != nil {
		return err
	}
	total := 0
	for _, l := range lacing {
		total += int(l)
	}
	body := make([]byte, total)
	if _, err := io.ReadFull(o.r, body); err != nil {
		return err
	}
	off := 0
	for _, l := range lacing {
		o.partial = append(o.partial, body[off:off+int(l)]...)
		off += int(l)
		if len(o.partial) > 1<<20 { // no Opus packet is anywhere near this
			o.partial = nil
			return errors.New("ogg: oversized packet")
		}
		if l < 255 {
			o.queue = append(o.queue, o.partial)
			o.partial = nil
		}
	}
	return nil
}

// OpusPacketDuration decodes the TOC byte to find the packet's play time.
func OpusPacketDuration(pkt []byte) time.Duration {
	if len(pkt) == 0 {
		return 10 * time.Millisecond
	}
	toc := pkt[0]
	config := toc >> 3
	var frame time.Duration
	switch {
	case config < 12: // SILK: 10/20/40/60 ms
		frame = []time.Duration{10, 20, 40, 60}[config%4] * time.Millisecond
	case config < 16: // Hybrid: 10/20 ms
		frame = []time.Duration{10, 20}[config%2] * time.Millisecond
	default: // CELT: 2.5/5/10/20 ms
		frame = []time.Duration{2500, 5000, 10000, 20000}[config%4] * time.Microsecond
	}
	frames := 1
	switch toc & 0x03 {
	case 1, 2:
		frames = 2
	case 3:
		if len(pkt) > 1 {
			frames = int(pkt[1] & 0x3f)
		}
	}
	return frame * time.Duration(frames)
}

// oggPage builds an Ogg page (test helper).
func oggPage(serial uint32, seq uint32, packets ...[]byte) []byte {
	var lacing, body []byte
	for _, p := range packets {
		n := len(p)
		for n >= 255 {
			lacing = append(lacing, 255)
			n -= 255
		}
		lacing = append(lacing, byte(n))
		body = append(body, p...)
	}
	hdr := make([]byte, 27)
	copy(hdr, "OggS")
	binary.LittleEndian.PutUint32(hdr[14:], serial)
	binary.LittleEndian.PutUint32(hdr[18:], seq)
	hdr[26] = byte(len(lacing))
	return append(append(hdr, lacing...), body...)
}

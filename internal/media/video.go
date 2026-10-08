package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"time"

	"github.com/pion/rtp"

	"github.com/coral-coder/windstream/internal/safe"
)

// VideoPacket is one RTP packet from the encoder.
type VideoPacket struct {
	Packet   *rtp.Packet
	Keyframe bool // first packet(s) of an IDR picture
}

// RunVideo starts ffmpeg and calls onPacket for every RTP packet until the
// context is cancelled or ffmpeg exits. It returns nil only on cancellation.
func RunVideo(ctx context.Context, cfg VideoConfig, codec, vendor string, src Source, log *slog.Logger, onPacket func(VideoPacket)) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return fmt.Errorf("listen for encoder RTP: %w", err)
	}
	defer conn.Close()
	_ = conn.SetReadBuffer(8 << 20)
	port := conn.LocalAddr().(*net.UDPAddr).Port
	rtpURL := fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", port)

	args := BuildVideoArgs(cfg, codec, vendor, src, rtpURL)
	cmd := exec.CommandContext(ctx, cfg.FFmpeg, args...)
	configureCmd(cmd)
	cmd.Stdout = io.Discard // the rtp muxer prints an SDP we do not need
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	log.Debug("starting ffmpeg video", "args", args)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	stderrDone := make(chan struct{})
	go func() {
		logStderr(stderr, log.With("pipeline", "video", "encoder", EncoderName(codec, vendor)))
		close(stderrDone)
	}()
	exited := make(chan error, 1)
	go func() {
		defer safe.Recover(log, "ffmpeg wait")
		waitForReader(stderrDone)
		exited <- cmd.Wait()
		conn.Close() // unblock the read loop
	}()

	buf := make([]byte, 1600)
	var sender *net.UDPAddr
	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() && ctx.Err() == nil {
				_ = cmd.Process.Kill()
				<-exited
				return errors.New("encoder produced no video for 10s (is the display asleep or the secure desktop showing?)")
			}
			break
		}
		// Only accept packets from the ffmpeg process we started (first
		// loopback sender wins).
		if !addr.IP.IsLoopback() {
			continue
		}
		if sender == nil {
			sender = addr
		} else if addr.Port != sender.Port {
			continue
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(append([]byte(nil), buf[:n]...)); err != nil {
			continue
		}
		onPacket(VideoPacket{Packet: pkt, Keyframe: IsKeyframeStart(codec, pkt)})
	}
	waitErr := <-exited
	if ctx.Err() != nil {
		return nil
	}
	if waitErr != nil {
		return fmt.Errorf("ffmpeg exited: %w", waitErr)
	}
	return errors.New("ffmpeg exited unexpectedly")
}

// Command windstream is the Windstream game streaming server for Windows.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/coral-coder/windstream/internal/auth"
	"github.com/coral-coder/windstream/internal/config"
	"github.com/coral-coder/windstream/internal/display"
	"github.com/coral-coder/windstream/internal/input"
	"github.com/coral-coder/windstream/internal/media"
	"github.com/coral-coder/windstream/internal/server"
	"github.com/coral-coder/windstream/internal/stream"
)

var version = "dev"

func defaultConfigPath() string {
	if v := os.Getenv("WINDSTREAM_CONFIG"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "Windstream", "windstream.toml")
		}
	}
	return "windstream.toml"
}

func usage() {
	fmt.Fprintf(os.Stderr, `windstream %s - authenticated HTTPS game streaming server for Windows

Usage:
  windstream serve         [-config PATH]       Run the server
  windstream check         [-config PATH]       Validate config and probe this PC
  windstream displays                           List monitors and GPUs (for display.monitor)
  windstream audio-devices                      List playback devices (for audio.device)
  windstream hash-password                      Hash a password for [[users]]
  windstream totp-setup    -user NAME           Generate a TOTP secret for a user
  windstream gen-cert      -hosts a,b -out DIR  Create a self-signed certificate
  windstream version

Default config: %s
`, version, defaultConfigPath())
}

func main() {
	if platformMain(os.Args[1:]) {
		return
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "app":
		err = cmdApp(os.Args[2:])
	case "check":
		err = cmdCheck(os.Args[2:])
	case "displays":
		err = cmdDisplays()
	case "audio-devices":
		err = cmdAudioDevices()
	case "hash-password":
		err = cmdHashPassword(os.Args[2:])
	case "totp-setup":
		err = cmdTOTPSetup(os.Args[2:])
	case "gen-cert":
		err = cmdGenCert(os.Args[2:])
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newLogger(cfg config.Log) (*slog.Logger, func(), error) {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(cfg.Level))
	opts := &slog.HandlerOptions{Level: lvl}
	var w io.Writer = os.Stderr
	closer := func() {}
	if cfg.File != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.File), 0o700); err != nil {
			return nil, nil, err
		}
		// Keep one previous log; rotate at 20 MB.
		if st, err := os.Stat(cfg.File); err == nil && st.Size() > 20<<20 {
			_ = os.Rename(cfg.File, cfg.File+".1")
		}
		f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file: %w", err)
		}
		w, closer = f, func() { _ = f.Close() }
	}
	if cfg.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts)), closer, nil
	}
	return slog.New(slog.NewTextHandler(w, opts)), closer, nil
}

func inputOptions(cfg *config.Config) input.Options {
	return input.Options{Gamepads: cfg.Input.Gamepads, Keyboard: cfg.Input.Keyboard, Mouse: cfg.Input.Mouse,
		MaxGamepads: cfg.Input.MaxGamepads}
}

func hubConfig(cfg *config.Config, disp *display.Manager) stream.Config {
	return stream.Config{
		Video: media.VideoConfig{
			FFmpeg: cfg.Video.FFmpegBinary, Width: cfg.Display.Width, Height: cfg.Display.Height,
			FPS: cfg.Video.FPS, BitrateKbps: cfg.Video.BitrateKbps, GOPFrames: cfg.Video.GOPFrames(),
			Encoder: cfg.Video.Encoder, Preset: cfg.Video.Preset, Profile: cfg.Video.Profile,
			ShowCursor: cfg.Video.ShowCursor, ExtraArgs: cfg.Video.ExtraArgs,
		},
		Source: func() (media.Source, error) {
			t, err := disp.Target()
			if err != nil {
				return media.Source{}, err
			}
			if t.Test {
				return media.Source{Test: true}, nil
			}
			adapter := t.Adapter
			if cfg.Display.Mode == "test" {
				adapter = cfg.Video.GPU
			}
			return media.Source{Adapter: adapter, Output: t.Output}, nil
		},
		Audio: media.AudioConfig{FFmpeg: cfg.Video.FFmpegBinary, Backend: cfg.Audio.Backend,
			Device: cfg.Audio.Device, BitrateKbps: cfg.Audio.BitrateKbps},
		AudioEnabled: cfg.Audio.Enabled,
		WebRTC:       cfg.WebRTC,
		Input:        inputOptions(cfg),
		InputEnabled: cfg.Input.Enabled,
		MaxClients:   cfg.Server.MaxClients,
	}
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	path := fs.String("config", defaultConfigPath(), "path to config file")
	logFile := fs.String("log-file", "", "append logs to this file (overrides log.file)")
	_ = fs.Parse(args)

	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if len(cfg.Users) == 0 {
		return errors.New("no [[users]] configured")
	}
	if *logFile != "" {
		cfg.Log.File = *logFile
	}
	log, closeLog, err := newLogger(cfg.Log)
	if err != nil {
		return err
	}
	defer closeLog()
	log.Info("windstream starting", "version", version, "config", *path)
	if runtime.GOOS == "windows" && !isElevated() {
		log.Warn("not running as Administrator: the virtual display cannot be toggled and input cannot reach elevated windows")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	disp := display.New(cfg.Display, log)
	defer disp.Stop() // also undoes a partially completed Start
	if err := disp.Start(ctx); err != nil {
		return err
	}

	hub, err := stream.NewHub(ctx, hubConfig(cfg, disp), log)
	if err != nil {
		return err
	}
	defer hub.Close()
	srv, err := server.New(cfg, hub, log)
	if err != nil {
		return err
	}
	if cfg.Input.Enabled {
		p := input.Check(inputOptions(cfg))
		if p.KeyboardMouse != nil {
			log.Warn("keyboard/mouse injection unavailable", "error", p.KeyboardMouse)
		}
		if cfg.Input.Gamepads && p.Gamepads != nil {
			log.Warn("gamepads unavailable", "error", p.Gamepads)
		}
	}
	err = srv.Run(ctx)
	log.Info("shutting down")
	return err
}

// cmdApp runs the full desktop app (dashboard, dependency setup, network
// automation) without installing it. With -dev it uses the synthetic source
// and touches nothing on the system, which works on any OS.
func cmdApp(args []string) error {
	fs := flag.NewFlagSet("app", flag.ExitOnError)
	dataDir := fs.String("data", "windstream-data", "data directory")
	dev := fs.Bool("dev", false, "test pattern, no drivers or router changes")
	panelAddr := fs.String("panel", "", "dashboard address (default: 127.0.0.1:47333, or a free port)")
	_ = fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runApp(ctx, appOptions{DataDir: *dataDir, Dev: *dev, PanelAddr: *panelAddr, Stderr: true})
}

func cmdCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	path := fs.String("config", defaultConfigPath(), "path to config file")
	_ = fs.Parse(args)

	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	ok := true
	report := func(good bool, label, detail string) {
		mark := "ok  "
		if !good {
			mark = "FAIL"
			ok = false
		}
		fmt.Printf("[%s] %-20s %s\n", mark, label, detail)
	}
	warn := func(label, detail string) { fmt.Printf("[warn] %-20s %s\n", label, detail) }

	report(true, "config", *path)
	if runtime.GOOS == "windows" {
		if isElevated() {
			report(true, "administrator", "yes")
		} else {
			warn("administrator", "no: run elevated so the virtual display can be toggled and input reaches elevated games/launchers")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if p, err := exec.LookPath(cfg.Video.FFmpegBinary); err != nil {
		report(false, "ffmpeg", err.Error()+" (install a full ffmpeg build, see docs/windows-setup.md)")
	} else {
		report(true, "ffmpeg", p)
		if cfg.Display.Mode != "test" {
			out, _ := exec.CommandContext(ctx, cfg.Video.FFmpegBinary, "-hide_banner", "-filters").Output()
			report(strings.Contains(string(out), " ddagrab "), "ddagrab capture", "Desktop Duplication filter (ffmpeg 6.0+)")
		}
		vc := media.VideoConfig{FFmpeg: cfg.Video.FFmpegBinary, Encoder: cfg.Video.Encoder}
		hardware := false
		for _, codec := range media.Codecs {
			cands, failed := media.Candidates(ctx, vc, codec)
			var names []string
			for _, v := range cands {
				names = append(names, media.EncoderName(codec, v))
				hardware = hardware || media.IsHardware(v)
			}
			if len(names) == 0 {
				names = []string{"none"}
			}
			report(len(cands) > 0 || codec != media.CodecH264, codec+" encoders", strings.Join(names, ", "))
			for _, e := range media.EncoderOrder {
				if err, bad := failed[e]; bad && cfg.Video.Encoder == "auto" {
					fmt.Printf("       %-6s unavailable: %s\n", e, firstLine(err.Error()))
				}
			}
		}
		if !hardware {
			warn("hardware encoding", "no GPU encoder works; software encoding costs a lot of CPU and latency")
		}
	}

	switch cfg.Display.Mode {
	case "virtual":
		st, err := display.VirtualDeviceStatus(cfg.Display.VirtualDevice)
		report(err == nil, "virtual display", orErr(st, err))
	case "monitor":
		outs, err := display.List()
		if err == nil {
			var o display.Output
			o, err = display.SelectOutput(outs, cfg.Display.Monitor)
			if err == nil {
				report(true, "monitor", fmt.Sprintf("%s %dx%d on %s", o.DeviceName, o.Width, o.Height, o.AdapterName))
				break
			}
		}
		report(false, "monitor", err.Error())
	case "test":
		warn("display", "test pattern mode: nothing on the desktop is streamed")
	}
	if len(cfg.Display.Launch) > 0 {
		_, err := exec.LookPath(cfg.Display.Launch[0])
		report(err == nil, "launch", orErr(strings.Join(cfg.Display.Launch, " "), err))
	}

	if cfg.Input.Enabled {
		p := input.Check(inputOptions(cfg))
		report(p.KeyboardMouse == nil, "keyboard/mouse", orErr("SendInput", p.KeyboardMouse))
		if cfg.Input.Gamepads {
			report(p.Gamepads == nil, "gamepads", orErr("ViGEmBus connected (virtual Xbox 360 controllers)", p.Gamepads))
		}
	}
	if cfg.Audio.Enabled {
		switch cfg.Audio.Backend {
		case "wasapi":
			devs, err := media.ListPlaybackDevices()
			if err != nil {
				report(false, "audio", err.Error())
			} else {
				target := "default playback device"
				found := cfg.Audio.Device == ""
				for _, d := range devs {
					if cfg.Audio.Device != "" && strings.Contains(strings.ToLower(d), strings.ToLower(cfg.Audio.Device)) {
						target, found = d, true
					}
				}
				report(found, "audio (loopback)", target)
			}
		default:
			report(true, "audio", cfg.Audio.Backend+" "+cfg.Audio.Device)
		}
	}
	switch {
	case cfg.TLS.CertFile != "":
		_, cerr := os.Stat(cfg.TLS.CertFile)
		_, kerr := os.Stat(cfg.TLS.KeyFile)
		report(cerr == nil && kerr == nil, "tls files", cfg.TLS.CertFile)
	case cfg.TLS.ACME:
		report(true, "tls", "ACME for "+strings.Join(cfg.TLS.ACMEDomains, ", "))
	case cfg.TLS.SelfSigned:
		warn("tls", "self_signed is for testing only")
	}
	for _, u := range cfg.Users {
		if u.TOTPSecret == "" {
			warn("user "+u.Name, "no TOTP configured (run `windstream totp-setup -user "+u.Name+"`)")
		}
	}
	if !ok {
		return errors.New("checks failed")
	}
	fmt.Println("all checks passed")
	return nil
}

func cmdDisplays() error {
	outs, err := display.List()
	if err != nil {
		return err
	}
	fmt.Printf("%-5s %-14s %-11s %-12s %-28s %s\n", "INDEX", "DEVICE", "SIZE", "POSITION", "MONITOR", "GPU (adapter)")
	for _, o := range outs {
		primary := ""
		if o.Primary {
			primary = " [primary]"
		}
		fmt.Printf("%-5d %-14s %-11s %-12s %-28s %s (%d)%s\n", o.Index, o.DeviceName,
			fmt.Sprintf("%dx%d", o.Width, o.Height), fmt.Sprintf("%d,%d", o.X, o.Y),
			truncate(o.MonitorName, 28), o.AdapterName, o.Adapter, primary)
	}
	return nil
}

func cmdAudioDevices() error {
	devs, err := media.ListPlaybackDevices()
	if err != nil {
		return err
	}
	for _, d := range devs {
		fmt.Println(d)
	}
	return nil
}

func readSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func cmdHashPassword(args []string) error {
	fs := flag.NewFlagSet("hash-password", flag.ExitOnError)
	_ = fs.Parse(args)
	pw, err := readSecret("Password: ")
	if err != nil {
		return err
	}
	if len(pw) < 12 {
		return errors.New("use at least 12 characters")
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		again, err := readSecret("Confirm:  ")
		if err != nil {
			return err
		}
		if again != pw {
			return errors.New("passwords do not match")
		}
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

func cmdTOTPSetup(args []string) error {
	fs := flag.NewFlagSet("totp-setup", flag.ExitOnError)
	user := fs.String("user", "", "account name (for the authenticator label)")
	issuer := fs.String("issuer", "Windstream", "issuer label")
	_ = fs.Parse(args)
	if *user == "" {
		return errors.New("-user is required")
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		return err
	}
	code, _ := auth.TOTPCode(secret, time.Now())
	fmt.Printf("Add this to the user's [[users]] entry:\n\n    totp_secret = %q\n\n", secret)
	fmt.Printf("Authenticator URL (paste into your app, or make a QR code from it):\n\n    %s\n\n", auth.TOTPURL(*issuer, *user, secret))
	fmt.Printf("Current code (to verify the app is in sync): %s\n", code)
	return nil
}

func cmdGenCert(args []string) error {
	fs := flag.NewFlagSet("gen-cert", flag.ExitOnError)
	hosts := fs.String("hosts", "localhost,127.0.0.1", "comma-separated DNS names and IPs")
	out := fs.String("out", "certs", "output directory")
	days := fs.Int("days", 397, "validity in days")
	_ = fs.Parse(args)
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return err
	}
	certPEM, keyPEM, err := server.GenerateSelfSigned(strings.Split(*hosts, ","), time.Duration(*days)*24*time.Hour)
	if err != nil {
		return err
	}
	certPath, keyPath := filepath.Join(*out, "cert.pem"), filepath.Join(*out, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s and %s\n\n[tls]\ncert_file = %q\nkey_file = %q\n", certPath, keyPath, certPath, keyPath)
	return nil
}

func orErr(ok string, err error) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(strings.TrimSpace(s), 110)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

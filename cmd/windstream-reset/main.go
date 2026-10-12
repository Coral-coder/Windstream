// Command windstream-reset (shipped as Windstream-Reset-Password.exe) sets a
// new password, and if needed a new authenticator, for a Windstream account.
// Run it on the PC where Windstream is installed.
package main

import (
	"bufio"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/term"

	"github.com/coral-coder/windstream/internal/pwreset"
)

var version = "dev"

func main() {
	dataDir := flag.String("data", defaultDataDir(), "Windstream's data folder")
	flag.Parse()

	if relaunched, err := elevate(); relaunched || err != nil {
		if err != nil {
			fmt.Fprintln(os.Stderr, "This needs administrator permission to change Windstream's settings:", err)
			pause()
			os.Exit(1)
		}
		return // the elevated copy did the work in its own window
	}

	in := bufio.NewReader(os.Stdin)
	var qrFiles []string
	defer func() {
		for _, f := range qrFiles {
			_ = os.Remove(f) // it holds the new authenticator key
		}
	}()
	err := pwreset.Run(filepath.Join(*dataDir, "windstream.toml"), pwreset.IO{
		In:  in,
		Out: os.Stdout,
		Secret: func(prompt string) (string, error) {
			fmt.Print(prompt)
			if term.IsTerminal(int(os.Stdin.Fd())) {
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Println()
				return string(b), err
			}
			line, err := in.ReadString('\n')
			if err != nil && line == "" {
				return "", err
			}
			return strings.TrimRight(line, "\r\n"), nil
		},
		ShowQR: func(url string) error {
			f, err := writeQRPage(url)
			if err != nil {
				return err
			}
			qrFiles = append(qrFiles, f)
			openInBrowser(f)
			return nil
		},
	})
	if err != nil {
		fmt.Println()
		fmt.Println("Password NOT changed:", err)
		pause()
		os.Exit(1)
	}
	pause()
}

// writeQRPage writes a small page showing the authenticator QR code.
func writeQRPage(otpauth string) (string, error) {
	png, err := qrcode.Encode(otpauth, qrcode.Medium, 320)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "windstream-authenticator-*.html")
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, `<!doctype html><meta charset="utf-8"><title>Windstream authenticator</title>
<body style="margin:0;min-height:100vh;display:grid;place-items:center;background:#071a1f;color:#ecf4f1;font:16px system-ui,sans-serif;text-align:center">
<div><h1 style="font-weight:600">Scan with your authenticator app</h1>
<img alt="QR code" style="background:#fff;padding:16px;border-radius:12px" src="data:image/png;base64,%s">
<p style="color:#9aaec1">Then type the 6-digit code it shows into the Windstream password reset window.<br>This page is deleted when the reset window closes.</p></div>`,
		base64.StdEncoding.EncodeToString(png))
	return f.Name(), err
}

func pause() {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}
	fmt.Print("\nPress Enter to close this window.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func init() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Windstream password reset %s\n\nRun it on the PC where Windstream is installed and follow the prompts.\n\n", version)
		flag.PrintDefaults()
	}
}

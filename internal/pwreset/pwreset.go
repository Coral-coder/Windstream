// Package pwreset is the "I forgot my password" tool: run on the gaming PC
// (as administrator, since the settings file is protected), it sets a new
// password for an account and, if the phone with the authenticator is gone
// too, a new authenticator. A running Windstream picks the change up within
// seconds and signs that account out everywhere.
package pwreset

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coral-coder/windstream/internal/auth"
	"github.com/coral-coder/windstream/internal/config"
)

// IO is how the tool talks to the person running it.
type IO struct {
	In  *bufio.Reader
	Out io.Writer
	// Secret reads a line without echoing it.
	Secret func(prompt string) (string, error)
	// ShowQR displays an otpauth:// URL as a QR code (e.g. in the browser).
	// It may be nil.
	ShowQR func(otpauthURL string) error
	// Now is the clock (tests).
	Now func() time.Time
}

// ErrNoAccounts means setup was never finished: there is nothing to reset.
var ErrNoAccounts = errors.New("there are no accounts yet: open Windstream and finish setup to create one")

// Run walks through the reset for the settings file at path.
func Run(path string, io IO) error {
	if io.Now == nil {
		io.Now = time.Now
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("Windstream's settings were not found at %s. Is Windstream installed on this PC?", path)
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		// Same repair Windstream itself does; keep a copy of the original.
		backup := path + ".broken-" + io.Now().Format("20060102-150405")
		_ = os.WriteFile(backup, data, 0o600)
		repaired, _ := config.Repair(data, config.AppDefaults())
		cfg = &repaired
	}
	if len(cfg.Users) == 0 {
		return ErrNoAccounts
	}

	fmt.Fprintln(io.Out, "Windstream password reset")
	fmt.Fprintln(io.Out, "=========================")
	fmt.Fprintln(io.Out)
	idx := 0
	if len(cfg.Users) == 1 {
		fmt.Fprintf(io.Out, "Account: %s\n\n", cfg.Users[0].Name)
	} else {
		fmt.Fprintln(io.Out, "Accounts:")
		for i, u := range cfg.Users {
			fmt.Fprintf(io.Out, "  %d. %s\n", i+1, u.Name)
		}
		for {
			line, err := ask(io, fmt.Sprintf("Which account? (1-%d): ", len(cfg.Users)))
			if err != nil {
				return err
			}
			if i := pick(cfg.Users, line); i >= 0 {
				idx = i
				break
			}
			fmt.Fprintln(io.Out, "  Type the number in front of the account.")
		}
		fmt.Fprintln(io.Out)
	}
	user := cfg.Users[idx]

	var password string
	for {
		pw, err := io.Secret("New password (at least 12 characters, it won't show as you type): ")
		if err != nil {
			return err
		}
		if len(pw) < 12 || len(pw) > 256 {
			fmt.Fprintln(io.Out, "  That's too short. Use at least 12 characters (a few words in a row is easy to remember).")
			continue
		}
		again, err := io.Secret("Type it again: ")
		if err != nil {
			return err
		}
		if again != pw {
			fmt.Fprintln(io.Out, "  Those didn't match. Let's try again.")
			continue
		}
		password = pw
		break
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	user.PasswordHash = hash

	fmt.Fprintln(io.Out)
	newTOTP := user.TOTPSecret == ""
	if !newTOTP {
		line, err := ask(io, "Do you still have the authenticator app with the Windstream code on your phone? [Y/n]: ")
		if err != nil {
			return err
		}
		newTOTP = strings.HasPrefix(strings.ToLower(line), "n")
	}
	if newTOTP {
		secret, err := setupTOTP(io, user.Name)
		if err != nil {
			return err
		}
		user.TOTPSecret = secret
	}

	cfg.Users[idx] = user
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("the settings would not be valid: %w", err)
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	fmt.Fprintln(io.Out)
	fmt.Fprintf(io.Out, "Done. Sign in as %q with your new password", user.Name)
	if newTOTP {
		fmt.Fprint(io.Out, " and the code from the new authenticator entry")
	}
	fmt.Fprintln(io.Out, ".")
	fmt.Fprintln(io.Out, "If Windstream is running it picks this up within a few seconds; no restart needed.")
	return nil
}

func setupTOTP(io IO, name string) (string, error) {
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		return "", err
	}
	url := auth.TOTPURL("Windstream", name, secret)
	fmt.Fprintln(io.Out, "Setting up a new authenticator code.")
	fmt.Fprintln(io.Out, "In your authenticator app (Google Authenticator, Microsoft Authenticator, 1Password, ...) add a new account:")
	if io.ShowQR != nil {
		if err := io.ShowQR(url); err == nil {
			fmt.Fprintln(io.Out, "  - scan the QR code that just opened in your browser, or")
		}
	}
	fmt.Fprintf(io.Out, "  - choose \"enter a setup key\" and type: %s\n", group(secret))
	fmt.Fprintln(io.Out, "(You can delete the old Windstream entry from the app; it no longer works.)")
	fmt.Fprintln(io.Out)
	for {
		code, err := ask(io, "Enter the 6-digit code the app now shows for Windstream: ")
		if err != nil {
			return "", err
		}
		code = strings.ReplaceAll(code, " ", "")
		if _, ok := auth.ValidateTOTP(secret, code, io.Now(), 1); ok {
			return secret, nil
		}
		fmt.Fprintln(io.Out, "  That code isn't right. Check the phone's clock is set automatically, and use the newest code.")
	}
}

// pick finds an account by its number in the list or by its name.
func pick(users []config.User, answer string) int {
	if n, err := strconv.Atoi(answer); err == nil {
		if n >= 1 && n <= len(users) {
			return n - 1
		}
		return -1
	}
	for i, u := range users {
		if strings.EqualFold(answer, u.Name) {
			return i
		}
	}
	return -1
}

// group spaces a key in fours so it is easy to type.
func group(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func ask(io IO, prompt string) (string, error) {
	fmt.Fprint(io.Out, prompt)
	line, err := io.In.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("cancelled")
	}
	return strings.TrimSpace(line), nil
}

//go:build !windows && !darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "windstream")
	}
	return "."
}

func elevate() (bool, error) { return false, nil } // file permissions decide

func openInBrowser(path string) { _ = exec.Command("xdg-open", path).Start() }

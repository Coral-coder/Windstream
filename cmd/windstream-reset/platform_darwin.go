//go:build darwin

package main

import (
	"os/exec"

	"github.com/coral-coder/windstream/internal/macapp"
)

func defaultDataDir() string    { return macapp.DataDir() }
func elevate() (bool, error)    { return false, nil } // the settings belong to the user
func openInBrowser(path string) { _ = exec.Command("/usr/bin/open", path).Start() }

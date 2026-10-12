//go:build windows

package main

import "github.com/coral-coder/windstream/internal/winapp"

func defaultDataDir() string    { return winapp.DataDir }
func elevate() (bool, error)    { return winapp.ElevateSelf() }
func openInBrowser(path string) { winapp.OpenInBrowser(path) }

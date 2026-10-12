//go:build !windows && !darwin

package main

func platformMain([]string) bool { return false }

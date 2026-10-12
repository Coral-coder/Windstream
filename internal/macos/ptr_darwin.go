//go:build darwin

package macos

import "unsafe"

// ptr turns a symbol address from dlsym into a pointer to read it.
func ptr(addr uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&addr)) //nolint:govet // address of a C global, not Go memory
}

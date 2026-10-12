//go:build !windows

package power

// KeepAwake is a no-op off Windows.
func KeepAwake() (release func()) { return func() {} }

// WakeDisplay is a no-op off Windows.
func WakeDisplay() {}

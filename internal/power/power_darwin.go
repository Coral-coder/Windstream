//go:build darwin

package power

import (
	"os/exec"
	"sync"
	"time"
)

// KeepAwake keeps the display and the Mac awake (macOS's own caffeinate)
// until the returned function is called.
func KeepAwake() (release func()) {
	cmd := exec.Command("/usr/bin/caffeinate", "-d", "-i", "-s")
	if err := cmd.Start(); err != nil {
		return func() {}
	}
	go func() { _ = cmd.Wait() }()
	var once sync.Once
	return func() { once.Do(func() { _ = cmd.Process.Kill() }) }
}

var (
	wakeMu   sync.Mutex
	lastWake time.Time
)

// WakeDisplay turns a sleeping display back on by declaring user activity.
// Calls closer than a second apart are folded into one.
func WakeDisplay() {
	wakeMu.Lock()
	if time.Since(lastWake) < time.Second {
		wakeMu.Unlock()
		return
	}
	lastWake = time.Now()
	wakeMu.Unlock()
	cmd := exec.Command("/usr/bin/caffeinate", "-u", "-t", "2")
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}

//go:build !windows

package media

import (
	"os/exec"
	"syscall"
	"time"
)

func configureCmd(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
}

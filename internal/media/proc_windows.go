//go:build windows

package media

import (
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// configureCmd hides ffmpeg's console window and raises its priority so the
// encoder is not starved by the game it is capturing.
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW | windows.ABOVE_NORMAL_PRIORITY_CLASS,
		HideWindow:    true,
	}
	cmd.WaitDelay = 3 * time.Second
}

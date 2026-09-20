//go:build windows

package ffmpeg

import (
	"os/exec"
	"syscall"
)

// Hide keeps ffmpeg from flashing a console window on screen.
func Hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

//go:build windows

package hls

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps ffmpeg from flashing a console window on screen.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

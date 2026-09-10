//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const daemonName = "dmd.exe"

// detach starts the daemon in its own process group with no console window,
// so closing the browser does not take the daemon down with it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	}
}

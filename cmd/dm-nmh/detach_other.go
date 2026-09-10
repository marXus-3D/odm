//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

const daemonName = "dmd"

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

//go:build !windows

package client

import (
	"os/exec"
	"syscall"
)

// DaemonName is the daemon binary this package looks for.
const DaemonName = "odmd"

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

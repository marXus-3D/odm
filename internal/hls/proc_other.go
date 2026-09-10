//go:build !windows

package hls

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}

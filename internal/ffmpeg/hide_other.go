//go:build !windows

package ffmpeg

import "os/exec"

func Hide(cmd *exec.Cmd) {}

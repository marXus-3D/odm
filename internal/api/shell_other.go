//go:build !windows

package api

import (
	"os/exec"
	"runtime"
)

func revealInFileManager(path string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-R", path).Start()
	}
	return exec.Command("xdg-open", path).Start()
}

func openWithDefaultApp(path string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", path).Start()
	}
	return exec.Command("xdg-open", path).Start()
}

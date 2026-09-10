//go:build windows

package api

import (
	"os/exec"
	"syscall"
)

// revealInFileManager opens Explorer with the file selected.
func revealInFileManager(path string) error {
	// explorer.exe is invoked directly, never through cmd.exe, so the path is
	// passed as a single argv entry and no shell metacharacter is interpreted.
	cmd := exec.Command("explorer.exe", "/select,"+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	// Explorer returns a non-zero exit code even on success, so its status is
	// deliberately ignored.
	_ = cmd.Start()
	return nil
}

// openWithDefaultApp launches the file with its registered handler.
func openWithDefaultApp(path string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

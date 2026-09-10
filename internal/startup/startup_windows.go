//go:build windows

// Package startup registers DM to run when the user logs in.
package startup

import (
	"os"
	"os/exec"
	"strings"
)

const runKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
const valueName = "DM Download Manager"

// command is what gets registered: the daemon, started in the background so
// logging in does not throw a window in the user's face.
func command() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return `"` + exe + `" -background`, nil
}

// Enabled reports whether DM is registered to run at login.
func Enabled() bool {
	out, err := exec.Command("reg", "query", runKey, "/v", valueName).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), valueName)
}

// Set adds or removes the login entry.
func Set(on bool) error {
	if !on {
		// Deleting a value that is not there is not a failure.
		exec.Command("reg", "delete", runKey, "/v", valueName, "/f").Run()
		return nil
	}
	cmd, err := command()
	if err != nil {
		return err
	}
	return exec.Command("reg", "add", runKey, "/v", valueName,
		"/t", "REG_SZ", "/d", cmd, "/f").Run()
}

// Supported reports whether this platform can register a login item.
func Supported() bool { return true }

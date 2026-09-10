//go:build windows

// Package startup registers ODM to run when the user logs in.
package startup

import (
	"os"
	"os/exec"
	"strings"
)

const runKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
const valueName = "Open Download Manager"

// Enabled reports whether ODM is registered to run at login.
func Enabled() bool {
	out, err := exec.Command("reg", "query", runKey, "/v", valueName).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), valueName)
}

// Set adds or removes the login entry for the running executable.
func Set(on bool) error {
	if !on {
		return SetPath("", false)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return SetPath(exe, true)
}

// SetPath registers a specific executable, which is what the installer
// needs: it must point at the installed daemon, not at itself.
func SetPath(exe string, on bool) error {
	if !on {
		// Deleting a value that is not there is not a failure.
		exec.Command("reg", "delete", runKey, "/v", valueName, "/f").Run()
		return nil
	}
	cmd := `"` + exe + `" -background`
	return exec.Command("reg", "add", runKey, "/v", valueName,
		"/t", "REG_SZ", "/d", cmd, "/f").Run()
}

// Supported reports whether this platform can register a login item.
func Supported() bool { return true }

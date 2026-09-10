//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/marcus/dm/internal/shortcut"
	"github.com/marcus/dm/internal/startup"
)

// Uninstall removes everything the installer put in place. userData says
// whether to delete the download list and settings as well.
func Uninstall(dir string, userData bool, say Reporter) error {
	say("Stopping DM...")
	stopRunning()

	id := manifestExtensionID(dir)

	say("Removing the browser integration...")
	setup := filepath.Join(dir, "bin", "dm-setup.exe")
	if _, err := os.Stat(setup); err == nil {
		exec.Command(setup, "-uninstall").Run()
	}
	unregisterExternalExtension(id)

	say("Removing shortcuts...")
	if programs, err := shortcut.StartMenuPrograms(); err == nil {
		os.Remove(filepath.Join(programs, appName+".lnk"))
	}
	if desktop, err := shortcut.Desktop(); err == nil {
		os.Remove(filepath.Join(desktop, appName+".lnk"))
	}

	say("Removing the start-up entry...")
	startup.SetPath("", false)

	say("Removing the Add or remove programs entry...")
	exec.Command("reg", "delete", uninstallKey, "/f").Run()

	if userData {
		if state := os.Getenv("APPDATA"); state != "" {
			say("Removing settings and the download list...")
			os.RemoveAll(filepath.Join(state, "dm"))
		}
	} else {
		say("Keeping your settings and download list.")
	}

	say("Removing program files...")
	if err := removeInstallDir(dir); err != nil {
		return err
	}
	say("")
	say("DM has been removed.")
	return nil
}

// removeInstallDir deletes the install folder. The running uninstaller
// lives inside it, so Windows will not let the last file go: everything
// else is deleted now and a detached shell removes the folder once this
// process has exited.
func removeInstallDir(dir string) error {
	self, _ := os.Executable()
	var failed []string

	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if sameFile(p, self) {
			return nil
		}
		if err := os.Remove(p); err != nil {
			failed = append(failed, p)
		}
		return nil
	})

	// Empty directories, deepest first.
	var dirs []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && p != dir {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i])
	}

	if err := os.Remove(dir); err == nil {
		return nil
	}
	scheduleSelfDelete(dir)
	return nil
}

func sameFile(a, b string) bool {
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}

// scheduleSelfDelete waits for this process to exit, then removes the
// folder that contains it.
//
// The command line is built by hand: Go escapes embedded quotes the way a
// C program expects, and cmd.exe does not read them that way, so a path
// with a space would silently break the delete. cmd is also started from
// the system root, because a shell whose working directory is inside the
// folder cannot remove it.
func scheduleSelfDelete(dir string) {
	quoted := `"` + dir + `"`
	line := `cmd /c ping 127.0.0.1 -n 3 >nul & rd /s /q ` + quoted +
		` & ping 127.0.0.1 -n 3 >nul & rd /s /q ` + quoted

	cmd := exec.Command("cmd")
	cmd.Dir = os.Getenv("SystemRoot")
	if cmd.Dir == "" {
		cmd.Dir = os.TempDir()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CmdLine:       line,
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED | NEW_PROCESS_GROUP
	}
	cmd.Start()
}

// FindInstallDir locates an existing installation, preferring the folder
// the uninstaller is running from.
func FindInstallDir() string {
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		if _, err := os.Stat(filepath.Join(dir, "bin", "dmd.exe")); err == nil {
			return dir
		}
	}
	out, err := exec.Command("reg", "query", uninstallKey, "/v", "InstallLocation").Output()
	if err == nil {
		s := string(out)
		if i := indexOf(s, "REG_SZ"); i >= 0 {
			v := trimSpace(s[i+len("REG_SZ"):])
			if v != "" {
				return v
			}
		}
	}
	return DefaultDir()
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' ||
		s[end-1] == '\r' || s[end-1] == '\n') {
		end--
	}
	return s[start:end]
}

// crxID reproduces Chrome's id derivation from a DER public key.
func crxID(der []byte) string {
	sum := sha256.Sum256(der)
	out := make([]byte, 32)
	for i := 0; i < 16; i++ {
		out[i*2] = 'a' + (sum[i] >> 4)
		out[i*2+1] = 'a' + (sum[i] & 0xf)
	}
	return string(out)
}

func decodeBase64(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("bad base64: %w", err)
	}
	return b, nil
}

//go:build windows

package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/marXus-3D/dm/internal/shortcut"
	"github.com/marXus-3D/dm/internal/startup"
)

// payload carries everything the installer writes to disk: the binaries,
// the unpacked extension, and the signed .crx when the build had a key.
//
//go:embed all:payload
var payload embed.FS

const (
	appName      = "DM Download Manager"
	uninstallID  = "DMDownloadManager"
	uninstallKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\` + uninstallID
)

// Options are the choices made in the wizard.
type Options struct {
	Dir          string
	RunAtLogin   bool
	DesktopIcon  bool
	SetUpBrowser bool
	LaunchAfter  bool
}

// DefaultDir is the per-user install location. It needs no administrator
// rights, and DM already keeps its registration in HKCU, so a machine-wide
// install would buy nothing.
func DefaultDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", "DM")
		}
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "Programs", "DM")
}

// Reporter receives progress lines for the wizard to display.
type Reporter func(format string, args ...any)

// Install performs the whole installation.
func Install(o Options, say Reporter) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("the installer only supports Windows")
	}

	say("Stopping any running copy of DM...")
	stopRunning()

	say("Copying files to %s", o.Dir)
	if err := extractPayload(o.Dir); err != nil {
		return fmt.Errorf("copy files: %w", err)
	}

	// The installer doubles as the uninstaller, so it lives alongside.
	self, err := os.Executable()
	if err == nil {
		if err := copyFile(self, filepath.Join(o.Dir, "DM-Setup.exe")); err != nil {
			say("  could not save the uninstaller: %v", err)
		}
	}

	daemon := filepath.Join(o.Dir, "bin", "dmd.exe")

	say("Creating shortcuts...")
	if err := makeShortcuts(o, daemon); err != nil {
		say("  %v", err)
	}

	if o.RunAtLogin {
		say("Setting DM to start when you sign in...")
		if err := startup.SetPath(daemon, true); err != nil {
			say("  %v", err)
		}
	} else {
		startup.SetPath("", false)
	}

	say("Registering with Add or remove programs...")
	if err := registerUninstall(o.Dir); err != nil {
		say("  %v", err)
	}

	if o.SetUpBrowser {
		say("Setting up the browser integration...")
		setupBrowser(o.Dir, say)
	}

	say("")
	say("Done. DM is installed in %s", o.Dir)
	return nil
}

// extractPayload writes the embedded tree into dir.
func extractPayload(dir string) error {
	return fs.WalkDir(payload, "payload", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "payload")
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" || d.IsDir() {
			if rel != "" {
				return os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o755)
			}
			return os.MkdirAll(dir, 0o755)
		}
		// .gitkeep only exists so the embed has something to match when the
		// payload has not been staged yet.
		if filepath.Base(rel) == ".gitkeep" {
			return nil
		}

		data, err := payload.ReadFile(p)
		if err != nil {
			return err
		}
		out := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return writeFileReplacing(out, data)
	})
}

// writeFileReplacing copes with a target that is running or locked by
// renaming it aside; Windows allows that where it forbids overwriting.
func writeFileReplacing(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o755); err == nil {
		return nil
	}
	old := path + ".old"
	os.Remove(old)
	if err := os.Rename(path, old); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		os.Rename(old, path)
		return err
	}
	os.Remove(old) // fails harmlessly while the old file is still mapped
	return nil
}

// stopRunning asks a running daemon to exit, then makes sure.
func stopRunning() {
	exec.Command("taskkill", "/F", "/IM", "dmd.exe").Run()
	exec.Command("taskkill", "/F", "/IM", "dm-nmh.exe").Run()
	time.Sleep(700 * time.Millisecond)
}

func makeShortcuts(o Options, daemon string) error {
	programs, err := shortcut.StartMenuPrograms()
	if err != nil {
		return err
	}
	err = shortcut.Create(shortcut.Spec{
		Path:        filepath.Join(programs, appName+".lnk"),
		Target:      daemon,
		Description: "Parallel download manager",
	})
	if err != nil {
		return fmt.Errorf("start menu shortcut: %w", err)
	}

	if o.DesktopIcon {
		desktop, err := shortcut.Desktop()
		if err != nil {
			return err
		}
		if err := shortcut.Create(shortcut.Spec{
			Path:        filepath.Join(desktop, appName+".lnk"),
			Target:      daemon,
			Description: "Parallel download manager",
		}); err != nil {
			return fmt.Errorf("desktop shortcut: %w", err)
		}
	}
	return nil
}

// registerUninstall adds the Add or remove programs entry.
func registerUninstall(dir string) error {
	self := filepath.Join(dir, "DM-Setup.exe")
	size := dirSizeKB(dir)
	vals := [][3]string{
		{"DisplayName", "REG_SZ", appName},
		{"DisplayVersion", "REG_SZ", Version},
		{"Publisher", "REG_SZ", "DM"},
		{"InstallLocation", "REG_SZ", dir},
		{"DisplayIcon", "REG_SZ", filepath.Join(dir, "bin", "dmd.exe")},
		{"UninstallString", "REG_SZ", `"` + self + `" -uninstall`},
		{"QuietUninstallString", "REG_SZ", `"` + self + `" -uninstall -silent`},
		{"NoModify", "REG_DWORD", "1"},
		{"NoRepair", "REG_DWORD", "1"},
		{"EstimatedSize", "REG_DWORD", fmt.Sprint(size)},
	}
	for _, v := range vals {
		out, err := exec.Command("reg", "add", uninstallKey, "/v", v[0],
			"/t", v[1], "/d", v[2], "/f").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %v: %s", v[0], err, out)
		}
	}
	return nil
}

func dirSizeKB(dir string) int64 {
	var total int64
	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total / 1024
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	return writeFileReplacing(dst, data)
}

// extensionVersion reads the version out of the packaged manifest.
func extensionVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "extension", "manifest.json"))
	if err != nil {
		return "0.1.0"
	}
	var m struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &m) != nil || m.Version == "" {
		return "0.1.0"
	}
	return m.Version
}

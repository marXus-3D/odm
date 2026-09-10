//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

// externalExtensionKeys are where each browser looks for extensions
// installed by another program on this machine.
var externalExtensionKeys = map[string]string{
	"Chrome":   `HKCU\Software\Google\Chrome\Extensions\`,
	"Edge":     `HKCU\Software\Microsoft\Edge\Extensions\`,
	"Brave":    `HKCU\Software\BraveSoftware\Brave-Browser\Extensions\`,
	"Chromium": `HKCU\Software\Chromium\Extensions\`,
	"Vivaldi":  `HKCU\Software\Vivaldi\Extensions\`,
}

// setupBrowser registers the native messaging host and then does what it
// can about the extension itself.
//
// The honest position on the extension: Chrome will not silently enable an
// extension that did not come from the Web Store. The registry entry below
// is the supported way to offer one, and Chrome may still leave it
// disabled or ignore it, so the guided path is always offered too.
func setupBrowser(dir string, say Reporter) {
	setup := filepath.Join(dir, "bin", "odm-setup.exe")
	if out, err := exec.Command(setup).CombinedOutput(); err != nil {
		say("  native messaging host: %v", err)
	} else {
		say("  native messaging host registered")
		if id := extensionIDFrom(out); id != "" {
			say("  extension id %s", id)
		}
	}

	id := manifestExtensionID(dir)
	crx := filepath.Join(dir, "odm.crx")
	if _, err := os.Stat(crx); err == nil && id != "" {
		n := registerExternalExtension(id, crx, extensionVersion(dir))
		say("  offered the packaged extension to %d browser(s)", n)
	}
}

// registerExternalExtension points browsers at the signed .crx. Returns how
// many registry entries were written.
func registerExternalExtension(id, crxPath, version string) int {
	count := 0
	for _, base := range externalExtensionKeys {
		key := base + id
		if exec.Command("reg", "add", key, "/v", "path", "/t", "REG_SZ",
			"/d", crxPath, "/f").Run() != nil {
			continue
		}
		if exec.Command("reg", "add", key, "/v", "version", "/t", "REG_SZ",
			"/d", version, "/f").Run() != nil {
			continue
		}
		count++
	}
	return count
}

func unregisterExternalExtension(id string) {
	if id == "" {
		return
	}
	for _, base := range externalExtensionKeys {
		exec.Command("reg", "delete", base+id, "/f").Run()
	}
}

// manifestExtensionID reads the id the packaged manifest will produce.
func manifestExtensionID(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "extension", "manifest.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(b, &m) != nil || m.Key == "" {
		return ""
	}
	der, err := decodeBase64(m.Key)
	if err != nil {
		return ""
	}
	return crxID(der)
}

// extensionIDFrom digs the id out of odm-setup's output, so the two agree
// even if the manifest could not be read.
func extensionIDFrom(out []byte) string {
	const marker = "extension id"
	s := string(out)
	i := indexOf(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i+len(marker):]
	start := -1
	for j := 0; j < len(rest); j++ {
		c := rest[j]
		if c >= 'a' && c <= 'p' {
			if start < 0 {
				start = j
			}
			if j-start == 31 {
				return rest[start : j+1]
			}
		} else if start >= 0 {
			start = -1
		}
	}
	return ""
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// OpenExtensionsPage puts the user in front of the extensions list, with
// the folder already on the clipboard for Load unpacked.
func OpenExtensionsPage(dir string) {
	extDir := filepath.Join(dir, "extension")
	setClipboard(extDir)

	for _, exe := range []string{"chrome.exe", "msedge.exe", "brave.exe"} {
		if _, err := exec.LookPath(exe); err != nil {
			continue
		}
		cmd := exec.Command(exe, "chrome://extensions")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if cmd.Start() == nil {
			return
		}
	}
	// No browser found on PATH: at least show the folder to load.
	cmd := exec.Command("explorer.exe", extDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Start()
}

// --- clipboard -------------------------------------------------------------

var (
	user32c            = syscall.NewLazyDLL("user32.dll")
	kernel32c          = syscall.NewLazyDLL("kernel32.dll")
	procOpenClipboard  = user32c.NewProc("OpenClipboard")
	procCloseClipboard = user32c.NewProc("CloseClipboard")
	procEmptyClipboard = user32c.NewProc("EmptyClipboard")
	procSetClipboard   = user32c.NewProc("SetClipboardData")
	procGlobalAlloc    = kernel32c.NewProc("GlobalAlloc")
	procGlobalLock     = kernel32c.NewProc("GlobalLock")
	procGlobalUnlock   = kernel32c.NewProc("GlobalUnlock")
)

// setClipboard copies text as CF_UNICODETEXT.
func setClipboard(text string) error {
	const (
		cfUnicodeText = 13
		gmemMoveable  = 0x0002
	)
	utf16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	size := uintptr(len(utf16) * 2)

	h, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return fmt.Errorf("GlobalLock failed")
	}
	dst := unsafe.Slice((*uint16)(winPtr(ptr)), len(utf16))
	copy(dst, utf16)
	procGlobalUnlock.Call(h)

	if ok, _, _ := procOpenClipboard.Call(0); ok == 0 {
		return fmt.Errorf("the clipboard is in use by another program")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	// Ownership of the memory passes to the clipboard on success.
	if ok, _, _ := procSetClipboard.Call(cfUnicodeText, h); ok == 0 {
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}

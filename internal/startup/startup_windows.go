//go:build windows

// Package startup registers ODM to run when the user logs in.
package startup

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// The login entry lives under HKCU, so none of this needs administrator
// rights.
//
// The registry is read and written through advapi32 rather than by shelling
// out to reg.exe. The daemon asks Enabled() on every state poll, which the
// desktop window makes several times a second; a subprocess that often is
// wasteful, and because the daemon is a GUI binary, each one flashed a
// console window on screen.
const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName  = "Open Download Manager"
)

var (
	advapi32           = syscall.NewLazyDLL("advapi32.dll")
	procRegOpenKeyEx   = advapi32.NewProc("RegOpenKeyExW")
	procRegQueryValue  = advapi32.NewProc("RegQueryValueExW")
	procRegSetValueEx  = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValue = advapi32.NewProc("RegDeleteValueW")
	procRegCloseKey    = advapi32.NewProc("RegCloseKey")
)

const (
	hkeyCurrentUser = 0x80000001
	keyQueryValue   = 0x0001
	keySetValue     = 0x0002
	regSZ           = 1
	errorSuccess    = 0
)

func utf16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		empty := uint16(0)
		return &empty
	}
	return p
}

// openRunKey opens HKCU\...\Run with the access the caller needs.
func openRunKey(access uintptr) (syscall.Handle, error) {
	path := utf16Ptr(runKeyPath)
	var h syscall.Handle
	r, _, _ := procRegOpenKeyEx.Call(
		hkeyCurrentUser,
		uintptr(unsafe.Pointer(path)),
		0,
		access,
		uintptr(unsafe.Pointer(&h)),
	)
	if r != errorSuccess {
		return 0, fmt.Errorf("open the Run key: error %d", r)
	}
	return h, nil
}

// Enabled reports whether ODM is registered to run at login.
func Enabled() bool {
	h, err := openRunKey(keyQueryValue)
	if err != nil {
		return false
	}
	defer procRegCloseKey.Call(uintptr(h))

	name := utf16Ptr(valueName)
	var size uint32
	// A nil buffer with a size out-parameter is the documented way to ask
	// only whether the value exists.
	r, _, _ := procRegQueryValue.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(name)),
		0, 0, 0,
		uintptr(unsafe.Pointer(&size)),
	)
	return r == errorSuccess
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
	h, err := openRunKey(keySetValue)
	if err != nil {
		return err
	}
	defer procRegCloseKey.Call(uintptr(h))

	name := utf16Ptr(valueName)
	if !on {
		// Deleting a value that is not there is not a failure.
		procRegDeleteValue.Call(uintptr(h), uintptr(unsafe.Pointer(name)))
		return nil
	}

	// -background so the login start goes to the tray instead of taking
	// focus; the window is created either way, just not shown.
	data, err := syscall.UTF16FromString(`"` + exe + `" -background`)
	if err != nil {
		return err
	}
	r, _, _ := procRegSetValueEx.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(name)),
		0,
		regSZ,
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)*2), // bytes, including the terminating NUL
	)
	if r != errorSuccess {
		return fmt.Errorf("write the Run value: error %d", r)
	}
	return nil
}

// Supported reports whether this platform can register a login item.
func Supported() bool { return true }

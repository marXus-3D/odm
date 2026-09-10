//go:build windows

// Package shortcut writes Windows .lnk files.
//
// A .lnk is a COM-serialised structure, so it is built through IShellLink
// and IPersistFile rather than by writing bytes. Doing it in-process keeps
// the installer from having to shell out to PowerShell for every shortcut.
package shortcut

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	ole32              = syscall.NewLazyDLL("ole32.dll")
	shell32            = syscall.NewLazyDLL("shell32.dll")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procCoUninitialize = ole32.NewProc("CoUninitialize")
	procCoCreate       = ole32.NewProc("CoCreateInstance")
	procSHGetFolder    = shell32.NewProc("SHGetFolderPathW")
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	clsidShellLink  = guid{0x00021401, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLinkW   = guid{0x000214F9, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile  = guid{0x0000010B, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	clsctxInprocAll = uintptr(1 | 4) // INPROC_SERVER | LOCAL_SERVER
)

// Indices into the IShellLinkW vtable, in declaration order.
const (
	slRelease             = 2
	slQueryInterface      = 0
	slSetDescription      = 7
	slSetWorkingDirectory = 9
	slSetArguments        = 11
	slSetIconLocation     = 17
	slSetPath             = 20
)

// Indices into the IPersistFile vtable.
const (
	pfRelease = 2
	pfSave    = 6
)

type comObject struct {
	vtbl **[64]uintptr
}

func (o *comObject) call(index int, args ...uintptr) uintptr {
	vtbl := *(**[64]uintptr)(unsafe.Pointer(o))
	fn := vtbl[index]
	all := append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	ret, _, _ := syscall.SyscallN(fn, all...)
	return ret
}

// Spec describes the shortcut to write.
type Spec struct {
	// Path is the .lnk file to create.
	Path string
	// Target is the program it points at.
	Target string
	Args   string
	// WorkingDir defaults to the target's directory.
	WorkingDir  string
	Description string
	// IconPath and IconIndex default to the target's own icon.
	IconPath  string
	IconIndex int32
}

// Create writes the shortcut, replacing any existing file.
func Create(s Spec) error {
	if s.WorkingDir == "" {
		s.WorkingDir = filepath.Dir(s.Target)
	}
	if s.IconPath == "" {
		s.IconPath = s.Target
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}

	// COINIT_APARTMENTTHREADED. A prior initialisation on this thread is
	// reported as S_FALSE, which is success.
	const coinitApartment = 2
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartment)
	if hr == 0 || hr == 1 { // S_OK or S_FALSE: we own the uninitialise
		defer procCoUninitialize.Call()
	}

	var link *comObject
	ret, _, _ := procCoCreate.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocAll,
		uintptr(unsafe.Pointer(&iidShellLinkW)),
		uintptr(unsafe.Pointer(&link)))
	if ret != 0 || link == nil {
		return fmt.Errorf("CoCreateInstance(ShellLink): 0x%x", ret)
	}
	defer link.call(slRelease)

	if hr := link.call(slSetPath, strPtr(s.Target)); hr != 0 {
		return fmt.Errorf("SetPath: 0x%x", hr)
	}
	if s.Args != "" {
		link.call(slSetArguments, strPtr(s.Args))
	}
	link.call(slSetWorkingDirectory, strPtr(s.WorkingDir))
	if s.Description != "" {
		link.call(slSetDescription, strPtr(s.Description))
	}
	link.call(slSetIconLocation, strPtr(s.IconPath), uintptr(s.IconIndex))

	var persist *comObject
	if hr := link.call(slQueryInterface,
		uintptr(unsafe.Pointer(&iidPersistFile)),
		uintptr(unsafe.Pointer(&persist))); hr != 0 || persist == nil {
		return fmt.Errorf("QueryInterface(IPersistFile): 0x%x", hr)
	}
	defer persist.call(pfRelease)

	if hr := persist.call(pfSave, strPtr(s.Path), 1 /*fRemember*/); hr != 0 {
		return fmt.Errorf("IPersistFile::Save: 0x%x", hr)
	}
	return nil
}

func strPtr(s string) uintptr {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return 0
	}
	return uintptr(unsafe.Pointer(p))
}

// Known folder ids used below.
const (
	csidlPrograms = 0x0002 // Start Menu\Programs
	csidlDesktop  = 0x0010 // Desktop
)

// StartMenuPrograms is the current user's Start Menu programs folder.
func StartMenuPrograms() (string, error) { return folder(csidlPrograms) }

// Desktop is the current user's desktop folder.
func Desktop() (string, error) { return folder(csidlDesktop) }

func folder(id int) (string, error) {
	buf := make([]uint16, 520)
	// SHGFP_TYPE_CURRENT = 0
	ret, _, _ := procSHGetFolder.Call(0, uintptr(id), 0, 0,
		uintptr(unsafe.Pointer(&buf[0])))
	if ret != 0 {
		return "", fmt.Errorf("SHGetFolderPath(%d): 0x%x", id, ret)
	}
	return syscall.UTF16ToString(buf), nil
}

//go:build windows

// Package trayicon puts ODM in the notification area.
//
// This is hand-rolled Win32 rather than a library because the machine has no
// C toolchain, and every Go systray package needs cgo. Everything here goes
// through syscall, so the daemon stays a single dependency-free binary.
package trayicon

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

//go:embed odm.ico
var iconBytes []byte

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassEx       = user32.NewProc("RegisterClassExW")
	procCreateWindowEx        = user32.NewProc("CreateWindowExW")
	procDefWindowProc         = user32.NewProc("DefWindowProcW")
	procDestroyWindow         = user32.NewProc("DestroyWindow")
	procGetMessage            = user32.NewProc("GetMessageW")
	procTranslateMessage      = user32.NewProc("TranslateMessage")
	procDispatchMessage       = user32.NewProc("DispatchMessageW")
	procPostQuitMessage       = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu       = user32.NewProc("CreatePopupMenu")
	procAppendMenu            = user32.NewProc("AppendMenuW")
	procDestroyMenu           = user32.NewProc("DestroyMenu")
	procTrackPopupMenu        = user32.NewProc("TrackPopupMenu")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procGetCursorPos          = user32.NewProc("GetCursorPos")
	procLoadImage             = user32.NewProc("LoadImageW")
	procDestroyIcon           = user32.NewProc("DestroyIcon")
	procPostMessage           = user32.NewProc("PostMessageW")
	procRegisterWindowMessage = user32.NewProc("RegisterWindowMessageW")
	procShellNotifyIcon       = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandle       = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmCommand     = 0x0111
	wmRButtonUp   = 0x0205
	wmLButtonUp   = 0x0202
	wmApp         = 0x8000
	wmTrayMessage = wmApp + 1

	nimAdd    = 0x0
	nimModify = 0x1
	nimDelete = 0x2

	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifInfo    = 0x10

	imageIcon      = 1
	lrLoadFromFile = 0x0010
	lrDefaultSize  = 0x0040

	mfString    = 0x0
	mfSeparator = 0x800
	mfGrayed    = 0x1

	tpmLeftAlign   = 0x0
	tpmRightButton = 0x2
	tpmReturnCmd   = 0x100
)

type point struct{ X, Y int32 }

type msg struct {
	HWnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   syscall.Handle
	Icon       syscall.Handle
	Cursor     syscall.Handle
	Background syscall.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     syscall.Handle
}

type notifyIconData struct {
	Size            uint32
	Wnd             syscall.Handle
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            syscall.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GuidItem        [16]byte
	BalloonIcon     syscall.Handle
}

// MenuItem is one entry in the tray's context menu.
type MenuItem struct {
	Label    string
	Disabled bool
	// Separator makes this a divider; Label and OnClick are ignored.
	Separator bool
	OnClick   func()
}

// Tray is a running notification-area icon.
type Tray struct {
	hwnd  syscall.Handle
	icon  syscall.Handle
	items []MenuItem
	quit  chan struct{}
	data  notifyIconData

	taskbarCreated uint32
}

var (
	activeMu sync.Mutex
	active   *Tray // the message loop needs to find the Tray from the WndProc
)

// StopActive ends the running tray, if there is one. It is safe to call from
// any goroutine: the window is owned by the thread running Run, so this asks
// that thread to close it rather than touching it directly.
func StopActive() {
	activeMu.Lock()
	t := active
	activeMu.Unlock()
	if t != nil {
		t.Stop()
	}
}

func setActive(t *Tray) {
	activeMu.Lock()
	active = t
	activeMu.Unlock()
}

func getActive() *Tray {
	activeMu.Lock()
	defer activeMu.Unlock()
	return active
}

// Run creates the tray icon and pumps messages until Stop is called.
//
// It must run on the goroutine that owns the window, and Windows requires
// that goroutine to stay on one OS thread; the caller is responsible for
// runtime.LockOSThread.
func Run(tooltip string, items []MenuItem) (*Tray, error) {
	iconPath, err := writeIcon()
	if err != nil {
		return nil, err
	}

	inst, _, _ := procGetModuleHandle.Call(0)
	className := utf16Ptr("DMTrayWindow")

	t := &Tray{items: items, quit: make(chan struct{})}
	setActive(t)

	wc := wndClassEx{
		Style:     0,
		WndProc:   syscall.NewCallback(wndProc),
		Instance:  syscall.Handle(inst),
		ClassName: className,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if ret, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
		return nil, err
	}

	// A plain hidden window rather than a message-only one: the shell will
	// not reliably deliver tray callbacks to HWND_MESSAGE windows.
	hwnd, _, err := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("ODM"))),
		0, 0, 0, 0, 0, 0, 0,
		inst, 0,
	)
	if hwnd == 0 {
		return nil, err
	}
	t.hwnd = syscall.Handle(hwnd)

	h, _, err := procLoadImage.Call(0, uintptr(unsafe.Pointer(utf16Ptr(iconPath))),
		imageIcon, 0, 0, lrLoadFromFile|lrDefaultSize)
	if h == 0 {
		return nil, errors.New("could not load the tray icon: " + err.Error())
	}
	t.icon = syscall.Handle(h)

	t.data = notifyIconData{
		Wnd:             t.hwnd,
		ID:              1,
		Flags:           nifMessage | nifIcon | nifTip,
		CallbackMessage: wmTrayMessage,
		Icon:            t.icon,
	}
	t.data.Size = uint32(unsafe.Sizeof(t.data))
	copyUTF16(t.data.Tip[:], tooltip)

	if ret, _, err := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&t.data))); ret == 0 {
		return nil, errors.New("could not add the tray icon: " + err.Error())
	}

	// Explorer restarts take every tray icon with them; this message is how
	// we learn to put ours back.
	tc, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(utf16Ptr("TaskbarCreated"))))
	t.taskbarCreated = uint32(tc)

	go func() {
		<-t.quit
		procPostMessage.Call(uintptr(t.hwnd), wmClose, 0, 0)
	}()

	t.pump()
	return t, nil
}

// pump is the window message loop.
func (t *Tray) pump() {
	var m msg
	for {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 { // 0 = WM_QUIT, -1 = error
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	t.cleanup()
}

func (t *Tray) cleanup() {
	if t.hwnd != 0 {
		procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&t.data)))
	}
	if t.icon != 0 {
		procDestroyIcon.Call(uintptr(t.icon))
		t.icon = 0
	}
	if t.hwnd != 0 {
		procDestroyWindow.Call(uintptr(t.hwnd))
		t.hwnd = 0
	}
}

// Stop tears the icon down and ends the message loop.
func (t *Tray) Stop() {
	select {
	case <-t.quit:
	default:
		close(t.quit)
	}
}

// Notify shows a balloon notification from the tray icon.
func (t *Tray) Notify(title, body string) {
	if t == nil || t.hwnd == 0 {
		return
	}
	d := t.data
	d.Flags = nifInfo
	copyUTF16(d.InfoTitle[:], title)
	copyUTF16(d.Info[:], body)
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&d)))
}

func wndProc(hwnd syscall.Handle, message uint32, wparam, lparam uintptr) uintptr {
	t := getActive()
	switch {
	case t != nil && message == t.taskbarCreated && t.taskbarCreated != 0:
		procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&t.data)))
		return 0

	case message == wmTrayMessage:
		switch uint32(lparam) {
		case wmLButtonUp:
			// Left click runs the first enabled item, which is the one
			// people expect a tray icon to do.
			if t != nil {
				for _, it := range t.items {
					if !it.Separator && !it.Disabled && it.OnClick != nil {
						go it.OnClick()
						break
					}
				}
			}
		case wmRButtonUp:
			if t != nil {
				t.showMenu()
			}
		}
		return 0

	case message == wmClose:
		procPostQuitMessage.Call(0)
		return 0

	case message == wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(message), wparam, lparam)
	return ret
}

func (t *Tray) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	for i, it := range t.items {
		if it.Separator {
			procAppendMenu.Call(menu, mfSeparator, 0, 0)
			continue
		}
		flags := uintptr(mfString)
		if it.Disabled {
			flags |= mfGrayed
		}
		// Command ids are 1-based; TrackPopupMenu returns 0 for "dismissed".
		procAppendMenu.Call(menu, flags, uintptr(i+1),
			uintptr(unsafe.Pointer(utf16Ptr(it.Label))))
	}

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Without this the menu will not close when the user clicks elsewhere.
	procSetForegroundWindow.Call(uintptr(t.hwnd))

	cmd, _, _ := procTrackPopupMenu.Call(menu,
		tpmLeftAlign|tpmRightButton|tpmReturnCmd,
		uintptr(pt.X), uintptr(pt.Y), 0, uintptr(t.hwnd), 0)

	if cmd == 0 {
		return
	}
	idx := int(cmd) - 1
	if idx >= 0 && idx < len(t.items) && t.items[idx].OnClick != nil {
		go t.items[idx].OnClick()
	}
}

// writeIcon materialises the embedded icon, because LoadImage wants a path.
func writeIcon() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "odm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, "odm-tray.ico")
	if st, err := os.Stat(path); err == nil && st.Size() == int64(len(iconBytes)) {
		return path, nil // already there and the right size
	}
	if err := os.WriteFile(path, iconBytes, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func utf16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		p, _ = syscall.UTF16PtrFromString("")
	}
	return p
}

func copyUTF16(dst []uint16, s string) {
	src := syscall.StringToUTF16(s)
	if len(src) > len(dst) {
		src = src[:len(dst)]
		src[len(src)-1] = 0
	}
	copy(dst, src)
}

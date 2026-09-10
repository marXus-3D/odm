//go:build windows

package nativeui

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/marXus-3D/dm/internal/client"
)

// Control ids for the extension prompt.
const (
	idEPPath = 4201 + iota
	idEPOpenFolder
	idEPOpenExtensions
	idEPLater
	idEPNever
)

// extPrompt tells the user the browser extension is not set up.
//
// Without it, downloads started in the browser never reach DM at all, which
// looks like DM being broken rather than a missing piece.
type extPrompt struct {
	hwnd    syscall.Handle
	extDir  string
	done    bool
	dismiss bool
}

var extPromptDlg *extPrompt

// maybePromptExtension shows the prompt once, if no browser has ever
// connected and the user has not waved it away.
func (a *App) maybePromptExtension(st *client.State) {
	if st.ExtensionSeen || st.Config.ExtensionPromptDismissed {
		return
	}
	a.mu.Lock()
	if a.extPrompted {
		a.mu.Unlock()
		return
	}
	a.extPrompted = true
	a.mu.Unlock()

	procPostMessage.Call(uintptr(a.hwnd), wmAppExtPrompt, 0, 0)
}

// showExtPrompt runs the prompt and reports whether to stop asking.
func (a *App) showExtPrompt() bool {
	inst, _, _ := procGetModuleHandle.Call(0)
	className := dialogClass("DMExtPrompt", syscall.NewCallback(extPromptProc), inst)

	d := &extPrompt{extDir: extensionDir()}
	extPromptDlg = d
	defer func() { extPromptDlg = nil }()

	const w, h = 560, 290
	hwnd, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("Browser extension not installed"))),
		wsCaption|wsSysMenu|wsVisible,
		cwUseDefault, cwUseDefault, w, h,
		uintptr(a.hwnd), 0, inst, 0)
	if hwnd == 0 {
		return false
	}
	d.hwnd = syscall.Handle(hwnd)
	applyDarkTitleBar(d.hwnd)

	mk := func(class, text string, style uintptr, x, y, cw, ch int32, id uintptr, ex uintptr) syscall.Handle {
		var t uintptr
		if text != "" {
			t = uintptr(unsafe.Pointer(utf16Ptr(text)))
		}
		h, _, _ := procCreateWindowEx.Call(ex,
			uintptr(unsafe.Pointer(utf16Ptr(class))), t,
			wsChild|wsVisible|style,
			uintptr(x), uintptr(y), uintptr(cw), uintptr(ch),
			hwnd, id, inst, 0)
		if h != 0 {
			procSendMessage.Call(h, wmSetFont, uintptr(a.font), 1)
			applyDarkControlTheme(syscall.Handle(h))
		}
		return syscall.Handle(h)
	}

	mk("STATIC", "No browser has connected to DM yet.", ssLeft, 16, 14, 500, 20, 0, 0)
	mk("STATIC", "Until the extension is loaded, downloads you start in your browser "+
		"will not come to DM.", ssLeft, 16, 36, 510, 36, 0, 0)

	mk("STATIC", "To set it up:", ssLeft, 16, 78, 200, 18, 0, 0)
	mk("STATIC", "1.  Open chrome://extensions and turn on Developer mode\r\n"+
		"2.  Choose \"Load unpacked\" and pick this folder:",
		ssLeft, 16, 98, 510, 38, 0, 0)

	mk("EDIT", d.extDir, esAutoHScroll|esReadOnly|wsTabStop,
		16, 140, 514, 23, idEPPath, wsExClientEdge)

	makeButton(syscall.Handle(hwnd), inst, a.font, "Open extensions page",
		16, 180, 150, 28, idEPOpenExtensions, true)
	makeButton(syscall.Handle(hwnd), inst, a.font, "Open this folder",
		172, 180, 130, 28, idEPOpenFolder, false)
	makeButton(syscall.Handle(hwnd), inst, a.font, "Later",
		340, 180, 90, 28, idEPLater, false)
	makeButton(syscall.Handle(hwnd), inst, a.font, "Don't ask again",
		436, 180, 94, 28, idEPNever, false)

	mk("STATIC", "DM keeps working without it -- you can still add URLs by hand.",
		ssLeft, 16, 220, 510, 20, 0, 0)

	procEnableWindow.Call(uintptr(a.hwnd), 0)

	var m msgStruct
	for !d.done {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		if m.Message == 0x0100 && m.WParam == 0x1B { // Escape
			d.finish(false)
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}

	procEnableWindow.Call(uintptr(a.hwnd), 1)
	procSetForegroundWindow.Call(uintptr(a.hwnd))
	return d.dismiss
}

func (d *extPrompt) finish(dismiss bool) {
	d.dismiss = dismiss
	d.done = true
	if d.hwnd != 0 {
		procDestroyWindow.Call(uintptr(d.hwnd))
		d.hwnd = 0
	}
}

func extPromptProc(hwnd syscall.Handle, message uint32, wparam, lparam uintptr) uintptr {
	d := extPromptDlg
	switch message {
	case wmCtlColorStatic, wmCtlColorBtn, wmCtlColorDlg,
		wmCtlColorEdit, wmCtlColorListBox:
		if brush, ok := darkCtlColor(message, wparam); ok {
			return brush
		}
	case wmDrawItem:
		if app != nil && drawDialogButton((*drawItemStruct)(lparamPtr(lparam)), app.font) {
			return 1
		}
	case wmCommand:
		if d == nil {
			break
		}
		switch uint32(wparam & 0xFFFF) {
		case idEPOpenExtensions:
			openExtensionsPage()
			return 0
		case idEPOpenFolder:
			openPath(d.extDir)
			return 0
		case idEPLater:
			d.finish(false)
			return 0
		case idEPNever:
			d.finish(true)
			return 0
		}
	case wmClose:
		if d != nil {
			d.finish(false)
		}
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(message), wparam, lparam)
	return ret
}

// extensionDir guesses where the unpacked extension lives: beside the
// binaries, which is how the repo is laid out.
func extensionDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "extension"
	}
	dir := filepath.Join(filepath.Dir(filepath.Dir(exe)), "extension")
	if _, err := os.Stat(dir); err == nil {
		return dir
	}
	return filepath.Join(filepath.Dir(exe), "extension")
}

// openExtensionsPage tries to put the user on chrome://extensions.
//
// ShellExecute will not follow a chrome:// URL, since no handler is
// registered for the scheme, so the browser is launched with it as an
// argument instead.
func openExtensionsPage() {
	for _, exe := range []string{"chrome.exe", "msedge.exe", "brave.exe"} {
		cmd := execCommand(exe, "chrome://extensions")
		if cmd == nil {
			continue
		}
		if err := cmd.Start(); err == nil {
			return
		}
	}
	// Nothing found: the folder is the next most useful thing.
	openPath(extensionDir())
}

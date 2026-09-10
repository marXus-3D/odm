//go:build windows

package nativeui

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/marcus/dm/internal/store"
)

// Control ids for the Download complete form.
const (
	idDCAddress = 4101 + iota
	idDCPath
	idDCOpen
	idDCOpenWith
	idDCFolder
	idDCClose
	idDCDontShow
)

// complete is the "Download complete" form, shown when a download finishes.
type complete struct {
	hwnd     syscall.Handle
	dontShow syscall.Handle
	rec      store.Record
	done     bool
	suppress bool
}

var completeDlg *complete

// showComplete reports a finished download. It returns true when the user
// asked not to see the dialog again.
func (a *App) showComplete(rec store.Record) bool {
	inst, _, _ := procGetModuleHandle.Call(0)
	className := dialogClass("DMComplete", syscall.NewCallback(completeProc), inst)

	d := &complete{rec: rec}
	completeDlg = d
	defer func() { completeDlg = nil }()

	// Tall enough for the checkbox below the buttons once the title bar and
	// borders are accounted for.
	const w, h = 470, 292
	hwnd, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("Download complete"))),
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

	name := rec.Filename
	if name == "" {
		name = filepath.Base(rec.Path)
	}
	mk("STATIC", "Download complete", ssLeft, 16, 14, 300, 20, 0, 0)
	mk("STATIC", fmt.Sprintf("Downloaded %s (%d bytes)", humanBytes(rec.Downloaded), rec.Downloaded),
		ssLeft, 16, 36, 420, 20, 0, 0)

	mk("STATIC", "Address", ssLeft, 16, 70, 200, 18, 0, 0)
	mk("EDIT", rec.URL, esAutoHScroll|esReadOnly|wsTabStop,
		16, 90, 424, 23, idDCAddress, wsExClientEdge)

	mk("STATIC", "The file saved as", ssLeft, 16, 122, 200, 18, 0, 0)
	mk("EDIT", rec.Path, esAutoHScroll|esReadOnly|wsTabStop,
		16, 142, 424, 23, idDCPath, wsExClientEdge)

	makeButton(syscall.Handle(hwnd), inst, a.font, "Open",
		16, 180, 95, 28, idDCOpen, true)
	makeButton(syscall.Handle(hwnd), inst, a.font, "Open with...",
		117, 180, 100, 28, idDCOpenWith, false)
	makeButton(syscall.Handle(hwnd), inst, a.font, "Open folder",
		223, 180, 100, 28, idDCFolder, false)
	makeButton(syscall.Handle(hwnd), inst, a.font, "Close",
		345, 180, 95, 28, idDCClose, false)

	d.dontShow = mk("BUTTON", "Don't show this dialog again",
		wsTabStop|bsAutoCheckBox, 16, 214, 250, 20, idDCDontShow, 0)

	// Modeless would be friendlier for many downloads at once, but a modal
	// form is what makes "don't show again" reachable without hunting.
	procEnableWindow.Call(uintptr(a.hwnd), 0)

	var m msgStruct
	for !d.done {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		if m.Message == 0x0100 && (m.WParam == 0x0D || m.WParam == 0x1B) {
			d.finish()
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}

	procEnableWindow.Call(uintptr(a.hwnd), 1)
	procSetForegroundWindow.Call(uintptr(a.hwnd))
	return d.suppress
}

func (d *complete) finish() {
	checked, _, _ := procSendMessage.Call(uintptr(d.dontShow), bmGetCheck, 0, 0)
	d.suppress = checked == bstChecked
	d.done = true
	if d.hwnd != 0 {
		procDestroyWindow.Call(uintptr(d.hwnd))
		d.hwnd = 0
	}
}

func completeProc(hwnd syscall.Handle, message uint32, wparam, lparam uintptr) uintptr {
	d := completeDlg
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
		case idDCOpen:
			openPath(d.rec.Path)
			d.finish()
			return 0
		case idDCOpenWith:
			openWith(d.hwnd, d.rec.Path)
			return 0
		case idDCFolder:
			revealPath(d.rec.Path)
			d.finish()
			return 0
		case idDCClose:
			d.finish()
			return 0
		}
	case wmClose:
		if d != nil {
			d.finish()
		}
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(message), wparam, lparam)
	return ret
}

// openWith shows the Windows "Open with" chooser.
func openWith(owner syscall.Handle, path string) {
	if path == "" {
		return
	}
	// The documented way to raise the chooser without writing a shell
	// extension: the OpenAs_RunDLL entry point.
	cmd := "shell32.dll,OpenAs_RunDLL " + path
	procShellExecute.Call(uintptr(owner),
		uintptr(unsafe.Pointer(utf16Ptr("open"))),
		uintptr(unsafe.Pointer(utf16Ptr("rundll32.exe"))),
		uintptr(unsafe.Pointer(utf16Ptr(cmd))),
		0, swShow)
}

// revealPath opens Explorer with the file selected.
func revealPath(path string) {
	if path == "" {
		return
	}
	procShellExecute.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr("open"))),
		uintptr(unsafe.Pointer(utf16Ptr("explorer.exe"))),
		uintptr(unsafe.Pointer(utf16Ptr("/select,"+path))),
		0, swShow)
}

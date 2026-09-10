//go:build windows

package nativeui

import (
	"sync"
	"syscall"
	"unsafe"
)

// This file makes the dialogs match the main window.
//
// Two Win32 facts drive the approach. Static text, edits and the dialog
// background can be recoloured by answering the WM_CTLCOLOR* messages. Push
// buttons cannot: a themed button paints itself from the system palette and
// ignores the parent entirely, so every button here is owner-drawn.

// dlgButton is a dialog push button drawn by us.
type dlgButton struct {
	label   string
	primary bool
	hovered bool
	hwnd    syscall.Handle
}

var (
	dlgBtnMu sync.Mutex
	dlgBtns  = map[uintptr]*dlgButton{} // keyed by control id
)

// makeButton creates a dark, owner-drawn push button.
func makeButton(parent syscall.Handle, inst uintptr, font syscall.Handle,
	label string, x, y, w, h int32, id uintptr, primary bool) syscall.Handle {

	hwnd, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr("BUTTON"))),
		uintptr(unsafe.Pointer(utf16Ptr(label))),
		wsChild|wsVisible|wsTabStop|bsOwnerDraw,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		uintptr(parent), id, inst, 0)
	if hwnd == 0 {
		return 0
	}
	procSendMessage.Call(hwnd, wmSetFont, uintptr(font), 1)

	b := &dlgButton{label: label, primary: primary, hwnd: syscall.Handle(hwnd)}
	dlgBtnMu.Lock()
	dlgBtns[id] = b
	dlgBtnMu.Unlock()
	procSetWindowSubclass.Call(hwnd, syscall.NewCallback(dlgButtonSubclass), id, 0)
	return syscall.Handle(hwnd)
}

func dlgButtonByID(id uintptr) *dlgButton {
	dlgBtnMu.Lock()
	defer dlgBtnMu.Unlock()
	return dlgBtns[id]
}

// dlgButtonSubclass gives dialog buttons a hover state, as on the toolbar.
func dlgButtonSubclass(hwnd syscall.Handle, msg uint32, wparam, lparam, id, ref uintptr) uintptr {
	b := dlgButtonByID(id)
	switch msg {
	case wmMouseMove:
		if b != nil && !b.hovered {
			b.hovered = true
			var tme trackMouseEvent
			tme.Size = uint32(unsafe.Sizeof(tme))
			tme.Flags = tmeLeave
			tme.TrackHwnd = hwnd
			procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
			procInvalidateRect.Call(uintptr(hwnd), 0, 1)
		}
	case wmMouseLeave:
		if b != nil && b.hovered {
			b.hovered = false
			procInvalidateRect.Call(uintptr(hwnd), 0, 1)
		}
	}
	ret, _, _ := procDefSubclassProc.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return ret
}

// drawDialogButton paints one dialog button. Returns false when the id is
// not one of ours, so a caller can fall through.
func drawDialogButton(dis *drawItemStruct, font syscall.Handle) bool {
	b := dlgButtonByID(uintptr(dis.CtlID))
	if b == nil {
		return false
	}
	hdc, r := dis.Hdc, dis.RcItem
	pressed := dis.ItemState&odsSelected != 0
	disabled := dis.ItemState&odsDisabled != 0
	focused := dis.ItemState&odsFocus != 0

	fillRect(hdc, r, colSurface)

	fill, textCol, outline := colSurface, colText, colBorder
	switch {
	case disabled:
		textCol = rgb(0x5A, 0x60, 0x6C)
	case b.primary && pressed:
		fill, textCol, outline = colAccentDim, colText, colAccentDim
	case b.primary && b.hovered:
		fill, textCol, outline = rgb(0x62, 0xA8, 0xFA), rgb(0x08, 0x0C, 0x12), rgb(0x62, 0xA8, 0xFA)
	case b.primary:
		fill, textCol, outline = colAccent, rgb(0x08, 0x0C, 0x12), colAccent
	case pressed:
		fill = colPressed
	case b.hovered:
		fill = colHover
	default:
		fill = rgb(0x23, 0x28, 0x31)
	}
	if focused && !b.primary {
		outline = colAccent
	}
	roundRect(hdc, r, 7, fill, outline)
	drawText(hdc, b.label, r, textCol, font, dtCenter|dtVCenter|dtSingleLine)
	return true
}

// darkCtlColor answers the WM_CTLCOLOR* family with the dark palette.
// The second result says whether the message was handled.
func darkCtlColor(msg uint32, wparam uintptr) (uintptr, bool) {
	switch msg {
	case wmCtlColorStatic, wmCtlColorBtn, wmCtlColorDlg:
		procSetBkMode.Call(wparam, transparent)
		procSetTextColor.Call(wparam, colText)
		procSetBkColor.Call(wparam, colSurface)
		return brushes.surface, true
	case wmCtlColorEdit, wmCtlColorListBox:
		procSetTextColor.Call(wparam, colText)
		procSetBkColor.Call(wparam, colList)
		return brushes.list, true
	}
	return 0, false
}

// dialogClass registers a window class already set up for the dark theme.
func dialogClass(name string, proc uintptr, inst uintptr) *uint16 {
	className := utf16Ptr(name)
	cursor, _, _ := procLoadCursor.Call(0, idcArrow)
	wc := wndClassEx{
		WndProc:    proc,
		Instance:   syscall.Handle(inst),
		Cursor:     syscall.Handle(cursor),
		Background: syscall.Handle(brushes.surface),
		ClassName:  className,
		Icon:       loadAppIcon(),
		IconSm:     loadAppIcon(),
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	// A repeat call for the same name fails with
	// ERROR_CLASS_ALREADY_EXISTS, which is expected and harmless: the
	// class from the first call is still registered.
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	return className
}

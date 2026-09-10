//go:build windows

package nativeui

import (
	"syscall"
	"unsafe"
)

// Toolbar glyphs, from the icon font Windows ships with. These code points
// exist in both Segoe Fluent Icons (Windows 11) and Segoe MDL2 Assets
// (Windows 10), so no image assets are needed.
const (
	glyphAdd      = "\uE710" // Add
	glyphPlay     = "\uE768" // Play
	glyphPause    = "\uE769" // Pause
	glyphStop     = "\uE71A" // Stop
	glyphDelete   = "\uE74D" // Delete
	glyphOpenFile = "\uE8E5" // OpenFile
	glyphFolder   = "\uE838" // FolderOpen
	glyphPlayAll  = "\uE895" // Sync, reading as "all of them"
	glyphMenu     = "\uE712" // More
)

// toolButton is one owner-drawn toolbar button.
type toolButton struct {
	label string
	glyph string
	cmd   uintptr
	// primary marks the accented call-to-action.
	primary bool
	// sepBefore puts a divider to the left of this button.
	sepBefore bool
	// rightAligned pins the button to the right edge of the toolbar.
	rightAligned bool

	hwnd    syscall.Handle
	hovered bool
	width   int32
}

// The toolbar carries the actions that apply to the selection. The bulk
// operations live in the menu instead: they are rarer, and nine buttons
// plus a menu did not fit at the default window width.
var toolButtons = []*toolButton{
	{label: "Add URL", glyph: glyphAdd, cmd: cmdAdd, primary: true, width: 96},
	{label: "Resume", glyph: glyphPlay, cmd: cmdResume, width: 86, sepBefore: true},
	{label: "Pause", glyph: glyphPause, cmd: cmdPause, width: 80},
	{label: "Remove", glyph: glyphDelete, cmd: cmdRemove, width: 88},
	{label: "Open", glyph: glyphOpenFile, cmd: cmdOpen, width: 76, sepBefore: true},
	{label: "Folder", glyph: glyphFolder, cmd: cmdReveal, width: 80},
	{label: "Menu", glyph: glyphMenu, cmd: cmdMenu, width: 82, rightAligned: true},
}

const (
	toolbarHeight = 52
	buttonHeight  = 34
	buttonTop     = (toolbarHeight - buttonHeight) / 2
	sepWidth      = 13
)

// buildToolbar creates the buttons. They are owner-drawn because a themed
// Win32 button cannot be made dark: it paints its own light background
// whatever the parent does.
func (a *App) buildToolbar(inst uintptr) {
	for _, b := range toolButtons {
		h, _, _ := procCreateWindowEx.Call(0,
			uintptr(unsafe.Pointer(utf16Ptr("BUTTON"))),
			uintptr(unsafe.Pointer(utf16Ptr(b.label))),
			wsChild|wsVisible|wsTabStop|bsOwnerDraw,
			0, buttonTop, uintptr(b.width), buttonHeight,
			uintptr(a.hwnd), b.cmd, inst, 0)
		if h == 0 {
			continue
		}
		b.hwnd = syscall.Handle(h)
		// Subclassed so the button can report hover; owner draw alone only
		// says pressed or focused.
		procSetWindowSubclass.Call(h, syscall.NewCallback(buttonSubclass), b.cmd, 0)
	}
	a.layoutToolbar(0)
}

// layoutToolbar positions the buttons. Most run left to right; the Menu
// button is pinned to the right edge, so it has to be repositioned every
// time the window is resized.
func (a *App) layoutToolbar(clientW int32) {
	if clientW == 0 {
		var r rect
		procGetClientRect.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(&r)))
		clientW = r.Right - r.Left
	}

	x := int32(10)
	for _, b := range toolButtons {
		if b.hwnd == 0 || b.rightAligned {
			continue
		}
		if b.sepBefore {
			x += sepWidth
		}
		procMoveWindow.Call(uintptr(b.hwnd), uintptr(x), buttonTop,
			uintptr(b.width), buttonHeight, 1)
		x += b.width + 4
	}
	a.toolbarWidth = x

	for _, b := range toolButtons {
		if b.hwnd == 0 || !b.rightAligned {
			continue
		}
		bx := clientW - b.width - 10
		if bx < x {
			bx = x // never let it overlap the action buttons
		}
		procMoveWindow.Call(uintptr(b.hwnd), uintptr(bx), buttonTop,
			uintptr(b.width), buttonHeight, 1)
	}
}

// buttonByHandle finds the button a message belongs to.
func buttonByCmd(cmd uintptr) *toolButton {
	for _, b := range toolButtons {
		if b.cmd == cmd {
			return b
		}
	}
	return nil
}

// buttonSubclass tracks the pointer so buttons can highlight under it.
func buttonSubclass(hwnd syscall.Handle, msg uint32, wparam, lparam, id, ref uintptr) uintptr {
	b := buttonByCmd(id)
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

// drawToolButton paints one button: a rounded plate, a glyph and a label.
func (a *App) drawToolButton(dis *drawItemStruct) {
	b := buttonByCmd(uintptr(dis.CtlID))
	if b == nil {
		return
	}
	hdc := dis.Hdc
	r := dis.RcItem
	pressed := dis.ItemState&odsSelected != 0
	disabled := dis.ItemState&odsDisabled != 0

	// The parent's colour first, so the rounded corners are not left with
	// whatever was underneath.
	fillRect(hdc, r, colSurface)

	fill, textCol := colSurface, colText
	outline := uintptr(0)
	switch {
	case disabled:
		textCol = rgb(0x5A, 0x60, 0x6C)
	case b.primary && pressed:
		fill, textCol = colAccentDim, colText
	case b.primary && b.hovered:
		fill, textCol = rgb(0x62, 0xA8, 0xFA), rgb(0x08, 0x0C, 0x12)
	case b.primary:
		fill, textCol = colAccent, rgb(0x08, 0x0C, 0x12)
	case pressed:
		fill, outline = colPressed, colBorder
	case b.hovered:
		fill, outline = colHover, colBorder
	}
	if fill != colSurface || outline != 0 {
		roundRect(hdc, r, 8, fill, outline)
	}

	// Glyph on the left, label after it, the pair centred as a unit.
	glyphW := int32(20)
	labelW := textWidth(hdc, b.label, a.font)
	total := glyphW + 6 + labelW
	startX := r.Left + (r.Right-r.Left-total)/2
	if startX < r.Left+8 {
		startX = r.Left + 8
	}

	if a.glyphFont != 0 && b.glyph != "" {
		gr := rect{Left: startX, Top: r.Top, Right: startX + glyphW, Bottom: r.Bottom}
		drawText(hdc, b.glyph, gr, textCol, a.glyphFont, dtCenter|dtVCenter|dtSingleLine)
	}
	lr := rect{Left: startX + glyphW + 6, Top: r.Top, Right: r.Right - 4, Bottom: r.Bottom}
	drawText(hdc, b.label, lr, textCol, a.font, dtLeft|dtVCenter|dtSingleLine)
}

// drawToolbarChrome fills the toolbar strip and its separators.
func (a *App) drawToolbarChrome(hdc syscall.Handle, clientW int32) {
	fillRect(hdc, rect{0, 0, clientW, toolbarHeight}, colSurface)
	// Hairline under the toolbar, to sit it apart from the list.
	fillRect(hdc, rect{0, toolbarHeight - 1, clientW, toolbarHeight}, colBorder)

	for _, b := range toolButtons {
		if !b.sepBefore || b.hwnd == 0 {
			continue
		}
		var wr rect
		procGetWindowRect.Call(uintptr(b.hwnd), uintptr(unsafe.Pointer(&wr)))
		var pt point
		pt.X, pt.Y = wr.Left, wr.Top
		procScreenToClient.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(&pt)))
		x := pt.X - sepWidth/2 - 2
		fillRect(hdc, rect{x, buttonTop + 6, x + 1, buttonTop + buttonHeight - 6}, colBorder)
	}
}

// textWidth measures a string in the given font.
func textWidth(hdc syscall.Handle, s string, font syscall.Handle) int32 {
	if s == "" {
		return 0
	}
	old, _, _ := procSelectObject.Call(uintptr(hdc), uintptr(font))
	r := rect{0, 0, 4000, 100}
	const dtCalcRect = 0x00000400
	procDrawTextEx.Call(uintptr(hdc), uintptr(unsafe.Pointer(utf16Ptr(s))), ^uintptr(0),
		uintptr(unsafe.Pointer(&r)), dtCalcRect|dtSingleLine|dtLeft, 0)
	procSelectObject.Call(uintptr(hdc), old)
	return r.Right - r.Left
}

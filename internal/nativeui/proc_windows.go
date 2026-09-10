//go:build windows

package nativeui

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/marcus/dm/internal/client"
)

func mainWndProc(hwnd syscall.Handle, message uint32, wparam, lparam uintptr) uintptr {
	a := app
	switch message {
	case wmSize:
		if a != nil && a.list != 0 {
			a.layout()
		}
		return 0

	case wmTimer:
		if a != nil {
			// The refresh talks to the daemon over HTTP. Doing that on the
			// UI thread would freeze the window whenever the daemon is slow,
			// so it runs elsewhere and posts wmAppRefresh when it has data.
			go a.refresh()
		}
		return 0

	case wmAppRefresh:
		if a != nil {
			a.applyPending()
		}
		return 0

	case wmCommand:
		if a != nil {
			a.onCommand(uint32(wparam & 0xFFFF))
		}
		return 0

	case wmNotify:
		if a != nil {
			if r, handled := a.onNotify(lparam); handled {
				return r
			}
		}

	case wmClose:
		// Closing hides the window and leaves the daemon running, the way
		// a download manager is expected to behave. Quitting for real is
		// File > Exit or the tray menu.
		procShowWindow.Call(uintptr(hwnd), swHide)
		return 0

	case wmAppQuit:
		procDestroyWindow.Call(uintptr(hwnd))
		return 0

	case wmDestroy:
		if a != nil {
			procKillTimer.Call(uintptr(a.hwnd), idTimer)
			a.hwnd = 0
		}
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(message), wparam, lparam)
	return ret
}

// onNotify handles list view notifications. The second result says whether
// the message was consumed.
func (a *App) onNotify(lparam uintptr) (uintptr, bool) {
	hdr := (*nmhdr)(lparamPtr(lparam))
	if hdr.HwndFrom != a.list {
		return 0, false
	}
	switch hdr.Code {
	case codeDblClk:
		rows := a.selected()
		if len(rows) == 1 {
			a.openOrResume(rows[0])
		}
		return 0, true

	case codeRClick:
		a.showContextMenu()
		return 0, true

	case codeCustomDraw:
		return a.onCustomDraw(lparam), true
	}
	return 0, false
}

// onCustomDraw paints a real progress bar in the Progress column.
//
// A percentage in text is legible but tells you nothing at a glance, which
// is most of the point of a download list.
func (a *App) onCustomDraw(lparam uintptr) uintptr {
	cd := (*nmLVCustomDraw)(lparamPtr(lparam))
	switch cd.Nmcd.DwDrawStage {
	case cddsPrePaint:
		return cdrfNotifyItemDraw
	case cddsItemPrePaint:
		return cdrfNotifySubItemDraw
	case cddsItemPrePaint | cddsSubItem:
		if cd.ISubItem != 2 {
			return cdrfDodefault
		}
		idx := int(cd.Nmcd.DwItemSpec)

		a.mu.Lock()
		var r row
		ok := idx >= 0 && idx < len(a.rows)
		if ok {
			r = a.rows[idx]
		}
		a.mu.Unlock()
		if !ok {
			return cdrfDodefault
		}

		var rc rect
		rc.Left = lvirBounds
		rc.Top = int32(cd.ISubItem)
		procSendMessage.Call(uintptr(a.list), lvmGetSubItemRect,
			uintptr(idx), uintptr(unsafe.Pointer(&rc)))

		a.drawProgress(cd.Nmcd.Hdc, rc, r)
		return cdrfSkipDefault
	}
	return cdrfDodefault
}

func (a *App) drawProgress(hdc syscall.Handle, rc rect, r row) {
	// Inset so the bar does not touch the grid lines.
	bar := rect{Left: rc.Left + 4, Top: rc.Top + 3, Right: rc.Right - 4, Bottom: rc.Bottom - 3}
	if bar.Right <= bar.Left || bar.Bottom <= bar.Top {
		return
	}

	trackBrush, _, _ := procCreateSolidBrush.Call(rgb(226, 229, 234))
	defer procDeleteObject.Call(trackBrush)
	procFillRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(&bar)), trackBrush)

	fillColor := rgb(79, 156, 249) // downloading
	switch r.State {
	case "done":
		fillColor = rgb(62, 175, 124)
	case "error":
		fillColor = rgb(226, 88, 106)
	case "paused":
		fillColor = rgb(230, 160, 40)
	}

	pct := r.Pct
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	width := int32(float64(bar.Right-bar.Left) * pct / 100)
	if width > 0 {
		fill := rect{Left: bar.Left, Top: bar.Top, Right: bar.Left + width, Bottom: bar.Bottom}
		brush, _, _ := procCreateSolidBrush.Call(fillColor)
		procFillRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(&fill)), brush)
		procDeleteObject.Call(brush)
	}

	edge, _, _ := procCreateSolidBrush.Call(rgb(188, 194, 204))
	procFrameRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(&bar)), edge)
	procDeleteObject.Call(edge)

	// The number goes on top of the bar, centred.
	label := formatPct(pct)
	procSetBkMode.Call(uintptr(hdc), transparent)
	procSetTextColor.Call(uintptr(hdc), rgb(28, 32, 40))
	old, _, _ := procSelectObject.Call(uintptr(hdc), uintptr(a.font))
	textRect := bar
	procDrawTextEx.Call(uintptr(hdc), uintptr(unsafe.Pointer(utf16Ptr(label))), ^uintptr(0),
		uintptr(unsafe.Pointer(&textRect)), dtCenter|dtVCenter|dtSingleLine, 0)
	if old != 0 {
		procSelectObject.Call(uintptr(hdc), old)
	}
}

func formatPct(p float64) string {
	if p >= 100 {
		return "100%"
	}
	return itoa(int(p)) + "%"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func rgb(r, g, b uint32) uintptr { return uintptr(r | g<<8 | b<<16) }

// --- actions ---------------------------------------------------------------

func (a *App) onCommand(id uint32) {
	switch id {
	case cmdAdd:
		a.promptAdd()
	case cmdExit:
		// Exit means stop DM entirely, not just close the window.
		if a.onQuit != nil {
			go a.onQuit()
		} else {
			Quit()
		}
	case cmdAbout:
		messageBox(a.hwnd, "About DM",
			"DM Download Manager\n\n"+
				"Parallel downloads with dynamic segmentation, resume,\n"+
				"HLS video, and browser integration.\n\n"+
				"Web UI: "+a.client.Base+"/", mbOk|mbIconInfo)
	case cmdWebUI:
		openURL(a.client.Base + "/")
	case cmdOpenFolder:
		if st, err := a.client.State(); err == nil {
			openPath(st.Config.Dir)
		}
	case cmdPauseAll:
		go func() {
			for _, r := range a.allRows() {
				if r.State == "downloading" || r.State == "probing" || r.State == "queued" {
					a.client.Pause(r.ID)
				}
			}
			a.refresh()
		}()
	case cmdPause, cmdResume, cmdOpen, cmdReveal, cmdRemove, cmdRemoveFile:
		a.applyToSelection(id)
	}
}

func (a *App) allRows() []row {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]row(nil), a.rows...)
}

func (a *App) applyToSelection(id uint32) {
	rows := a.selected()
	if len(rows) == 0 {
		return
	}
	if id == cmdRemoveFile {
		if messageBox(a.hwnd, "Remove and delete",
			"Delete the downloaded file from disk as well?", mbYesNo|mbIconError) != idYes {
			return
		}
	}
	go func() {
		for _, r := range rows {
			switch id {
			case cmdPause:
				a.client.Pause(r.ID)
			case cmdResume:
				a.client.Resume(r.ID)
			case cmdOpen:
				a.client.Open(r.ID)
			case cmdReveal:
				a.client.Reveal(r.ID)
			case cmdRemove:
				a.client.Remove(r.ID, false)
			case cmdRemoveFile:
				a.client.Remove(r.ID, true)
			}
		}
		a.refresh()
	}()
}

// openOrResume is the double-click action: finished downloads open, anything
// else toggles between running and paused.
func (a *App) openOrResume(r row) {
	go func() {
		switch r.State {
		case "done":
			a.client.Open(r.ID)
		case "downloading", "probing", "queued":
			a.client.Pause(r.ID)
		default:
			a.client.Resume(r.ID)
		}
		a.refresh()
	}()
}

func (a *App) showContextMenu() {
	rows := a.selected()
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	add := func(id uintptr, label string, enabled bool) {
		flags := uintptr(mfString)
		if !enabled {
			flags |= mfGrayed
		}
		procAppendMenu.Call(menu, flags, id, uintptr(unsafe.Pointer(utf16Ptr(label))))
	}

	has := len(rows) > 0
	add(cmdResume, "Resume", has)
	add(cmdPause, "Pause", has)
	procAppendMenu.Call(menu, mfSeparator, 0, 0)
	add(cmdOpen, "Open file", has)
	add(cmdReveal, "Show in Explorer", has)
	procAppendMenu.Call(menu, mfSeparator, 0, 0)
	add(cmdRemove, "Remove from list", has)
	add(cmdRemoveFile, "Remove and delete file", has)

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(a.hwnd))
	cmd, _, _ := procTrackPopupMenu.Call(menu,
		tpmLeftAlign|tpmRightButton|tpmReturnCmd,
		uintptr(pt.X), uintptr(pt.Y), 0, uintptr(a.hwnd), 0)
	if cmd != 0 {
		a.onCommand(uint32(cmd))
	}
}

// iconPath returns the icon the tray package wrote, reused for the window.
func iconPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	p := filepath.Join(dir, "dm", "dm-tray.ico")
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

var _ = client.AddRequest{}

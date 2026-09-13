//go:build windows

package nativeui

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/marXus-3D/odm/internal/client"
	"github.com/marXus-3D/odm/internal/power"
	"github.com/marXus-3D/odm/internal/store"
)

func mainWndProc(hwnd syscall.Handle, message uint32, wparam, lparam uintptr) uintptr {
	a := app
	switch message {
	case wmSize:
		if a != nil && a.list != 0 {
			// Width changed, so the right-anchored Menu button moves too.
			a.layoutToolbar(int32(lparam & 0xFFFF))
			a.layout()
		}
		return 0

	case wmEraseBkgnd:
		// Painting is handled entirely in WM_PAINT; letting the default
		// erase happen first causes a visible flash on resize.
		return 1

	case wmPaint:
		if a != nil {
			a.paintChrome()
			return 0
		}

	case wmDrawItem:
		if a != nil {
			dis := (*drawItemStruct)(lparamPtr(lparam))
			a.drawToolButton(dis)
			return 1
		}

	// Every control that asks about its colours gets the dark palette;
	// without this the dialogs come back light grey in patches.
	case wmCtlColorStatic, wmCtlColorBtn, wmCtlColorDlg:
		procSetBkMode.Call(wparam, transparent)
		procSetTextColor.Call(wparam, colText)
		procSetBkColor.Call(wparam, colSurface)
		return brushes.surface

	case wmCtlColorEdit, wmCtlColorListBox:
		procSetTextColor.Call(wparam, colText)
		procSetBkColor.Call(wparam, colList)
		return brushes.list

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

	case wmAppDialog:
		if a != nil {
			a.drainDialogs()
		}
		return 0

	case wmAppExtPrompt:
		if a != nil && a.showExtPrompt() {
			off := true
			go func() {
				a.client.SetFlags(client.Flags{ExtensionPromptDismissed: &off})
			}()
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
			// The per-download windows are top level, so nothing tears them
			// down with the main one.
			a.closeProgressWindows()
			a.hwnd = 0
		}
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(message), wparam, lparam)
	return ret
}

// paintChrome draws the toolbar strip and the status bar. The list view
// paints itself in between.
func (a *App) paintChrome() {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(&ps)))

	var cr rect
	procGetClientRect.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(&cr)))
	w, h := cr.Right-cr.Left, cr.Bottom-cr.Top

	a.drawToolbarChrome(syscall.Handle(hdc), w)

	// Status bar.
	top := h - statusHeight
	if top < toolbarHeight {
		top = toolbarHeight
	}
	fillRect(syscall.Handle(hdc), rect{0, top, w, h}, colSurface)
	fillRect(syscall.Handle(hdc), rect{0, top, w, top + 1}, colBorder)

	a.mu.Lock()
	text := a.status
	a.mu.Unlock()
	if text != "" {
		drawText(syscall.Handle(hdc), text,
			rect{12, top, w - 12, h}, colTextDim, a.smallFont,
			dtLeft|dtVCenter|dtSingleLine|dtEndEllipsis)
	}
}

// drainDialogs shows the queued modal dialogs one at a time.
//
// Each one runs its own nested message loop, so this must never be
// re-entered: a second finish arriving while a dialog is up would otherwise
// stack another on top of it.
func (a *App) drainDialogs() {
	a.mu.Lock()
	if a.dialogOpen {
		a.mu.Unlock()
		return
	}
	a.dialogOpen = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.dialogOpen = false
		more := len(a.pendingConfirm) > 0 || len(a.pendingComplete) > 0
		a.mu.Unlock()
		if more {
			procPostMessage.Call(uintptr(a.hwnd), wmAppDialog, 0, 0)
		}
	}()

	for {
		a.mu.Lock()
		var rec store.Record
		var kind string
		switch {
		case len(a.pendingConfirm) > 0:
			rec, a.pendingConfirm = a.pendingConfirm[0], a.pendingConfirm[1:]
			kind = "confirm"
		case len(a.pendingComplete) > 0:
			rec, a.pendingComplete = a.pendingComplete[0], a.pendingComplete[1:]
			kind = "complete"
		}
		cfg := a.cfg
		a.mu.Unlock()

		switch kind {
		case "confirm":
			a.runFileInfo(rec, cfg)
		case "complete":
			a.runComplete(rec)
		default:
			return
		}
	}
}

// runFileInfo shows the Download File Info form and applies the answer.
func (a *App) runFileInfo(rec store.Record, cfg store.Config) {
	Show() // the dialog is useless behind a hidden window
	answer, ok, remember := a.showFileInfo(rec, cfg)
	if !ok {
		// Cancelled: the download was never started, so drop it entirely.
		go func() {
			a.client.Remove(rec.ID, false)
			a.refresh()
		}()
		return
	}
	go func() {
		if remember && answer.Dir != "" && answer.Category != "" {
			a.rememberCategoryDir(answer.Category, answer.Dir)
		}
		if err := a.client.Confirm(rec.ID, answer); err != nil {
			messageBox(a.hwnd, "Download",
				"Could not start the download:\n\n"+err.Error(), mbOk|mbIconError)
		}
		a.refresh()
	}()
}

// rememberCategoryDir makes the chosen folder the default for a category.
func (a *App) rememberCategoryDir(category, dir string) {
	st, err := a.client.State()
	if err != nil {
		return
	}
	cats := append([]store.Category(nil), st.Config.Categories...)
	for i := range cats {
		if cats[i].Name == category {
			cats[i].Dir = dir
			if _, err := a.client.SetConfig(store.Config{Categories: cats}); err != nil {
				return
			}
			return
		}
	}
}

// runComplete announces a finished download.
func (a *App) runComplete(rec store.Record) {
	if a.showComplete(rec) {
		off := false
		go func() {
			a.client.SetFlags(client.Flags{ShowCompleteDialog: &off})
			a.refresh()
		}()
	}
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
		// Alternate row shading, and a distinct selection colour, because
		// the list view's own defaults are tuned for a light theme.
		idx := int(cd.Nmcd.DwItemSpec)
		bg := colList
		if idx%2 == 1 {
			bg = colRowAlt
		}
		selected, _, _ := procSendMessage.Call(uintptr(a.list), lvmGetItemState,
			uintptr(idx), lvisSelected)
		if selected != 0 {
			bg = colSelect
		}
		fillRect(cd.Nmcd.Hdc, cd.Nmcd.Rc, bg)
		cd.ClrTextBk = uint32(bg)
		cd.ClrText = uint32(colText)
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

	roundRect(hdc, bar, 7, colTrack, 0)

	fillColor := colAccent
	switch r.State {
	case "done":
		fillColor = colOK
	case "error":
		fillColor = colError
	case "paused":
		fillColor = colWarn
	case "queued", "confirm":
		fillColor = colTextDim
	}

	pct := r.Pct
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	width := int32(float64(bar.Right-bar.Left) * pct / 100)
	if width > 8 {
		fill := rect{Left: bar.Left, Top: bar.Top, Right: bar.Left + width, Bottom: bar.Bottom}
		roundRect(hdc, fill, 7, fillColor, 0)
	} else if width > 0 {
		// Too narrow to round without looking like a smudge.
		fillRect(hdc, rect{bar.Left, bar.Top + 2, bar.Left + width, bar.Bottom - 2}, fillColor)
	}

	// The number sits on the bar, in whichever colour stays legible over
	// the filled portion.
	label := formatPct(pct)
	textCol := colText
	if pct > 55 {
		textCol = rgb(0x0A, 0x0E, 0x14)
	}
	drawText(hdc, label, bar, textCol, a.smallFont, dtCenter|dtVCenter|dtSingleLine)
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
	case cmdMenu:
		a.showMainMenu()
	case cmdExit:
		// Exit means stop ODM entirely, not just close the window.
		if a.onQuit != nil {
			go a.onQuit()
		} else {
			Quit()
		}
	case cmdAbout:
		messageBox(a.hwnd, "About ODM",
			"Open Download Manager\n\n"+
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
		go func() { a.client.PauseAll(); a.refresh() }()
	case cmdResumeAll:
		go func() { a.client.ResumeAll(); a.refresh() }()
	case cmdStopAll:
		go func() { a.client.StopAll(); a.refresh() }()
	case cmdStartWithWindows, cmdShowStartDialog, cmdShowCompleteDialog, cmdShowProgressDialog:
		a.toggleFlag(id)
	case cmdPause, cmdResume, cmdOpen, cmdReveal, cmdRemove, cmdRemoveFile:
		a.applyToSelection(id)
	default:
		acts := power.Actions()
		if idx := int(id) - cmdOnFinishBase; idx >= 0 && idx < len(acts) {
			a.setOnFinish(acts[idx])
		}
	}
}

// setOnFinish records what should happen once the queue drains.
func (a *App) setOnFinish(act power.Action) {
	go func() {
		if act == power.None {
			// Choosing "do nothing" also calls off anything already
			// scheduled with the OS, which is what a user means by it.
			power.CancelPending()
		}
		if _, err := a.client.SetConfig(store.Config{OnComplete: string(act)}); err != nil {
			messageBox(a.hwnd, "Settings",
				"Could not save the setting:\n\n"+err.Error(), mbOk|mbIconError)
			return
		}
		a.refresh()
	}()
}

// toggleFlag flips one of the Options switches.
func (a *App) toggleFlag(id uint32) {
	go func() {
		st, err := a.client.State()
		if err != nil {
			return
		}
		var f client.Flags
		switch id {
		case cmdStartWithWindows:
			v := !st.StartWithWindows
			f.StartWithWindows = &v
		case cmdShowStartDialog:
			v := !st.Config.ShowStartDialog
			f.ShowStartDialog = &v
		case cmdShowCompleteDialog:
			v := !st.Config.ShowCompleteDialog
			f.ShowCompleteDialog = &v
		case cmdShowProgressDialog:
			v := !st.Config.ShowProgressDialog
			f.ShowProgressDialog = &v
			if !v {
				a.closeProgressWindows()
			}
		}
		if _, err := a.client.SetFlags(f); err != nil {
			messageBox(a.hwnd, "Settings",
				"Could not save the setting:\n\n"+err.Error(), mbOk|mbIconError)
			return
		}
		a.refresh()
	}()
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
	p := filepath.Join(dir, "odm", "odm-tray.ico")
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

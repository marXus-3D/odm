//go:build windows

package nativeui

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

// A small window per running download, the way IDM does it: the file, how
// far along it is, how fast, how long is left, and buttons to pause, cancel
// or just get it out of the way.
//
// These are modeless, unlike the File Info and Complete forms. Several can
// be open at once and the main window stays usable, so there is no nested
// message loop here: the window procedure is driven by the main loop in Run,
// and the rows that arrive on every refresh are pushed in from applyPending.

// Control ids are allocated per window rather than fixed, because the
// owner-drawn button registry in darkdlg is keyed by id alone. Two windows
// sharing an id would share a label, so "Pause" on one would rename the
// other.
const (
	progressIDBase = 5000
	progressIDStep = 8
)

// Button offsets inside one window's block of ids. Kept in their own const
// block: sharing one with the constants above would make iota continue from
// there and silently shift every offset.
const (
	offPause = iota
	offCancel
	offHide
)

type progressWin struct {
	hwnd   syscall.Handle
	id     string // download id
	idBase uintptr

	pauseBtn syscall.Handle

	// row is the last state received. Only the UI thread touches it.
	row row
	// paused tracks what the Pause button currently says, so the label is
	// only rewritten when it actually changes.
	paused bool
}

var (
	progressMu   sync.Mutex
	progressWins = map[syscall.Handle]*progressWin{}
	progressSeq  uintptr
)

func progressByHwnd(h syscall.Handle) *progressWin {
	progressMu.Lock()
	defer progressMu.Unlock()
	return progressWins[h]
}

// isRunning reports whether a download is in a state worth a window.
func isRunning(state string) bool {
	switch state {
	case "downloading", "probing", "queued", "paused":
		return true
	}
	return false
}

// syncProgressWindows opens, updates and closes the per-download windows to
// match the latest rows. It runs on the UI thread, from applyPending.
func (a *App) syncProgressWindows(rows []row) {
	a.mu.Lock()
	enabled := a.cfg.ShowProgressDialog
	a.mu.Unlock()

	if a.progress == nil {
		a.progress = map[string]*progressWin{}
	}

	live := make(map[string]bool, len(rows))
	for _, r := range rows {
		// A download only earns a window once it is actually going. Queued
		// and paused keep theirs so the buttons stay reachable, but a fresh
		// queued row does not open one on its own.
		if w, ok := a.progress[r.ID]; ok {
			// Finished, failed or removed: leave it out of live so the
			// sweep below closes it. The Complete form takes over.
			if !isRunning(r.State) {
				continue
			}
			live[r.ID] = true
			w.update(r)
			continue
		}
		if !enabled || !a.started {
			continue
		}
		if r.State != "downloading" && r.State != "probing" {
			continue
		}
		// Opened once per download: closing the window is a decision, and
		// reopening it on the next poll would undo it.
		if a.progressShown[r.ID] {
			continue
		}
		a.progressShown[r.ID] = true
		if w := a.newProgressWin(r); w != nil {
			a.progress[r.ID] = w
			live[r.ID] = true
		}
	}

	// Anything finished, failed, removed or no longer listed loses its
	// window; the Complete form takes over from there.
	for id, w := range a.progress {
		if !live[id] {
			w.close()
			delete(a.progress, id)
		}
	}
}

func (a *App) newProgressWin(r row) *progressWin {
	inst, _, _ := procGetModuleHandle.Call(0)
	className := dialogClass("DMProgress", syscall.NewCallback(progressProc), inst)

	progressMu.Lock()
	progressSeq++
	idBase := progressIDBase + progressSeq*progressIDStep
	progressMu.Unlock()

	w := &progressWin{id: r.ID, idBase: idBase, row: r}

	const width, height = 520, 330
	// Cascade, so a second download does not land exactly on the first.
	offset := int32((progressSeq % 6) * 26)
	hwnd, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr(progressTitle(r)))),
		wsCaption|wsSysMenu|wsClipChildren,
		uintptr(120+offset), uintptr(120+offset), width, height,
		0, 0, inst, 0)
	if hwnd == 0 {
		return nil
	}
	w.hwnd = syscall.Handle(hwnd)
	applyDarkTitleBar(w.hwnd)

	progressMu.Lock()
	progressWins[w.hwnd] = w
	progressMu.Unlock()

	font := a.font
	const btnY, btnW, btnH = 252, 96, 30
	w.pauseBtn = makeButton(w.hwnd, inst, font, "Pause", 180, btnY, btnW, btnH,
		idBase+offPause, true)
	makeButton(w.hwnd, inst, font, "Cancel", 286, btnY, btnW, btnH, idBase+offCancel, false)
	makeButton(w.hwnd, inst, font, "Hide", 392, btnY, btnW, btnH, idBase+offHide, false)

	w.applyPausedLabel(r.State == "paused")
	procShowWindow.Call(hwnd, swShowNormal)
	procUpdateWindow.Call(hwnd)
	return w
}

func progressTitle(r row) string {
	name := r.Name
	if name == "" {
		name = shortURL(r.URL)
	}
	if name == "" {
		name = "Download"
	}
	return name
}

// update takes a fresh row and repaints.
func (w *progressWin) update(r row) {
	if w.hwnd == 0 {
		return
	}
	w.row = r
	w.applyPausedLabel(r.State == "paused")
	procInvalidateRect.Call(uintptr(w.hwnd), 0, 0)
}

// applyPausedLabel keeps the primary button reading the action it performs.
func (w *progressWin) applyPausedLabel(paused bool) {
	if paused == w.paused && w.pauseBtn != 0 {
		return
	}
	w.paused = paused
	label := "Pause"
	if paused {
		label = "Resume"
	}
	if b := dlgButtonByID(w.idBase + offPause); b != nil {
		b.label = label
	}
	if w.pauseBtn != 0 {
		procInvalidateRect.Call(uintptr(w.pauseBtn), 0, 1)
	}
}

func (w *progressWin) close() {
	if w.hwnd == 0 {
		return
	}
	h := w.hwnd
	w.hwnd = 0
	progressMu.Lock()
	delete(progressWins, h)
	progressMu.Unlock()
	procDestroyWindow.Call(uintptr(h))
}

// paint draws the whole window: there are no static controls, which keeps
// the colours consistent and the flicker down.
func (w *progressWin) paint() {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&ps)))
	dc := syscall.Handle(hdc)

	var cr rect
	procGetClientRect.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&cr)))
	fillRect(dc, cr, colSurface)

	a := app
	if a == nil {
		return
	}
	r := w.row

	const labelX, valueX, right = 18, 130, 502
	y := int32(16)
	line := func(label, value string) {
		drawText(dc, label, rect{labelX, y, valueX - 8, y + 18}, colTextDim,
			a.smallFont, dtLeft|dtVCenter|dtSingleLine)
		drawText(dc, value, rect{valueX, y, right, y + 18}, colText,
			a.smallFont, dtLeft|dtVCenter|dtSingleLine)
		y += 22
	}

	line("File name", progressTitle(r))
	// The whole URL, trimmed to fit: shortURL would leave only the file
	// name, which the line above already shows.
	line("Address", ellipsize(r.URL, 58))
	line("Status", progressStatus(r))

	size := "unknown"
	if r.Size > 0 {
		size = humanBytes(r.Size)
	}
	line("Downloaded", fmt.Sprintf("%s of %s", humanBytes(r.Done), size))

	speed := "-"
	if r.SpeedBPS > 0 {
		speed = humanBytes(int64(r.SpeedBPS)) + "/s"
	}
	line("Transfer rate", speed)

	left := r.ETA
	if left == "" {
		left = "-"
	}
	line("Time left", left)

	conns := "-"
	if r.Conns > 0 {
		conns = fmt.Sprintf("%d", r.Conns)
	}
	line("Connections", conns)

	// The overall bar.
	y += 6
	bar := rect{labelX, y, right, y + 22}
	roundRect(dc, bar, 4, colTrack, colBorder)
	pct := r.Pct
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	inner := bar
	inner.Left++
	inner.Top++
	inner.Bottom--
	width := float64(inner.Right-1-inner.Left) * pct / 100
	if width > 0 {
		filled := inner
		filled.Right = inner.Left + int32(width)
		fill := colAccent
		if r.State == "paused" {
			fill = colWarn
		}
		roundRect(dc, filled, 3, fill, 0)
	}
	drawText(dc, fmt.Sprintf("%.1f%%", pct), bar, colText, a.smallFont,
		dtCenter|dtVCenter|dtSingleLine)
	y += 30

	// One strip per connection, showing where each has reached in its own
	// slice of the file. This is the part that makes eight connections
	// visible rather than a claim.
	if len(r.Segments) > 0 && r.Size > 0 {
		drawText(dc, "Each connection's share of the file", rect{labelX, y, right, y + 16},
			colTextDim, a.smallFont, dtLeft|dtVCenter|dtSingleLine)
		y += 18
		strip := rect{labelX, y, right, y + 14}
		fillRect(dc, strip, colTrack)
		total := float64(r.Size)
		scale := float64(strip.Right-strip.Left) / total
		for _, s := range r.Segments {
			if s.End <= s.Start {
				continue
			}
			x0 := strip.Left + int32(float64(s.Start)*scale)
			xCur := strip.Left + int32(float64(s.Cur)*scale)
			xEnd := strip.Left + int32(float64(s.End)*scale)
			// Its share of the file, then how much of that is on disk.
			fillRect(dc, rect{x0, strip.Top, xEnd, strip.Bottom}, colRowAlt)
			if xCur > x0 {
				fillRect(dc, rect{x0, strip.Top, xCur, strip.Bottom}, colOK)
			}
			// A hairline so neighbouring ranges stay distinguishable.
			fillRect(dc, rect{xEnd - 1, strip.Top, xEnd, strip.Bottom}, colBorder)
		}
	}
}

// ellipsize keeps a long value inside the window.
func ellipsize(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	// Keep the tail: the host is guessable, the file name is the part that
	// identifies the download.
	return s[:max/3] + "..." + s[len(s)-(max-max/3-3):]
}

func progressStatus(r row) string {
	switch r.State {
	case "downloading":
		return "Downloading"
	case "probing":
		return "Connecting"
	case "paused":
		return "Paused"
	case "queued":
		return "Queued"
	case "done":
		return "Complete"
	case "error":
		return "Failed"
	}
	return r.State
}

// command handles the three buttons. The client calls go to a goroutine so a
// slow daemon cannot freeze the window.
func (w *progressWin) command(id uintptr) bool {
	a := app
	if a == nil {
		return false
	}
	switch id - w.idBase {
	case offPause:
		paused := w.paused
		dlID := w.id
		go func() {
			if paused {
				a.client.Resume(dlID)
			} else {
				a.client.Pause(dlID)
			}
			a.refresh()
		}()
		// Flip immediately so the button does not look stuck for a poll.
		w.applyPausedLabel(!paused)
		return true

	case offCancel:
		// Stop the transfer and let go of the window. The download stays in
		// the list, paused, with its partial file: cancelling a dialog
		// should not throw away what has already been fetched.
		dlID := w.id
		go func() {
			a.client.Pause(dlID)
			a.refresh()
		}()
		a.forgetProgress(dlID)
		return true

	case offHide:
		a.forgetProgress(w.id)
		return true
	}
	return false
}

// forgetProgress closes a window and stops it being reopened.
func (a *App) forgetProgress(id string) {
	if w, ok := a.progress[id]; ok {
		w.close()
		delete(a.progress, id)
	}
}

// closeProgressWindows tears them all down, for shutdown.
func (a *App) closeProgressWindows() {
	for id, w := range a.progress {
		w.close()
		delete(a.progress, id)
	}
}

func progressProc(hwnd syscall.Handle, message uint32, wparam, lparam uintptr) uintptr {
	w := progressByHwnd(hwnd)
	switch message {
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		if w != nil {
			w.paint()
			return 0
		}
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
		if w != nil && w.command(wparam&0xFFFF) {
			return 0
		}
	case wmClose:
		// The close box hides the window. It does not stop the download:
		// that is what Cancel is for.
		if w != nil && app != nil {
			app.forgetProgress(w.id)
		}
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(message), wparam, lparam)
	return ret
}

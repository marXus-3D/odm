//go:build windows

package nativeui

import (
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/marcus/dm/internal/client"
	"github.com/marcus/dm/internal/manager"
	"github.com/marcus/dm/internal/store"
)

// Command ids for menu items and buttons.
const (
	cmdAdd = 1000 + iota
	cmdPause
	cmdResume
	cmdRemove
	cmdRemoveFile
	cmdOpen
	cmdReveal
	cmdOpenFolder
	cmdWebUI
	cmdExit
	cmdAbout
	cmdPauseAll
	cmdResumeAll
	cmdStopAll
	cmdStartWithWindows
	cmdShowStartDialog
	cmdShowCompleteDialog
	cmdSelectAll
)

// wmAppRefresh asks the UI thread to apply rows fetched elsewhere.
// wmAppQuit tears the window down for real, as opposed to hiding it.
const (
	wmAppRefresh = wmApp + 1
	wmAppQuit    = wmApp + 2
	wmAppDialog  = wmApp + 3
)

const (
	idListView = 2000
	idTimer    = 1
	refreshMs  = 700
)

// column describes one list view column.
type column struct {
	title string
	width int32
	align int32
}

var columns = []column{
	{"Name", 300, lvcfmtLeft},
	{"Size", 90, lvcfmtRight},
	{"Progress", 150, lvcfmtLeft},
	{"Speed", 90, lvcfmtRight},
	{"Status", 110, lvcfmtLeft},
	{"Time left", 80, lvcfmtRight},
}

// App is the desktop window.
type App struct {
	hwnd    syscall.Handle
	list    syscall.Handle
	font    syscall.Handle
	buttons []syscall.Handle

	client      *client.Client
	optionsMenu syscall.Handle

	mu      sync.Mutex
	rows    []row
	pending []row
	dirty   bool
	cfg     store.Config

	// seenDone remembers which downloads have already had their completion
	// dialog, so a finished row does not reopen it on every refresh.
	seenDone map[string]bool
	// dialogOpen serialises the modal dialogs; without it a burst of
	// finishes would try to stack several at once.
	dialogOpen bool
	// pendingConfirm are downloads waiting on the Download File Info form,
	// pendingComplete those whose completion has not been announced.
	pendingConfirm  []store.Record
	pendingComplete []store.Record

	// started guards the first refresh, so opening the app does not replay
	// a completion dialog for everything already in the list.
	started bool

	// progress is what the custom-draw handler paints, kept separate so the
	// draw path never blocks on a network call.
	onQuit func()
}

// row is one download as the list shows it.
type row struct {
	ID       string
	Name     string
	Size     int64
	Done     int64
	State    string
	SpeedBPS float64
	ETA      string
	Pct      float64
	Path     string
	Created  time.Time
}

var app *App // the window procedure needs to reach the App

// Run opens the window and pumps messages until it is closed.
//
// It must be called on a goroutine locked to its OS thread: Windows delivers
// messages to the thread that created the window.
func Run(c *client.Client, onQuit func()) error {
	enableVisualStyles()
	initCommonControls()

	a := &App{client: c, onQuit: onQuit, seenDone: map[string]bool{}}
	app = a

	inst, _, _ := procGetModuleHandle.Call(0)
	className := utf16Ptr("DMMainWindow")
	cursor, _, _ := procLoadCursor.Call(0, idcArrow)
	bg, _, _ := procGetSysColorBrush.Call(colorBtnFace)

	wc := wndClassEx{
		Style:      0,
		WndProc:    syscall.NewCallback(mainWndProc),
		Instance:   syscall.Handle(inst),
		Cursor:     syscall.Handle(cursor),
		Background: syscall.Handle(bg),
		ClassName:  className,
		Icon:       loadAppIcon(),
		IconSm:     loadAppIcon(),
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if ret, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
		return fmt.Errorf("register window class: %w", err)
	}

	hwnd, _, err := procCreateWindowEx.Call(
		wsExControlParent,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("DM Download Manager"))),
		wsOverlappedWin|wsClipChildren,
		cwUseDefault, cwUseDefault, 900, 460,
		0, 0, inst, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("create window: %w", err)
	}
	a.hwnd = syscall.Handle(hwnd)
	a.font = uiFont()

	a.buildMenu()
	a.buildChildren(inst)
	a.refresh()
	a.mu.Lock()
	a.started = true
	a.mu.Unlock()

	procSetTimer.Call(uintptr(a.hwnd), idTimer, refreshMs, 0)
	procShowWindow.Call(uintptr(a.hwnd), swShowNormal)
	procUpdateWindow.Call(uintptr(a.hwnd))

	var m msgStruct
	for {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	return nil
}

// Show brings the window back up, for the tray's "Open DM". It is safe to
// call from another thread.
func Show() bool {
	if app == nil || app.hwnd == 0 {
		return false
	}
	h := uintptr(app.hwnd)
	if iconic, _, _ := procIsIconic.Call(h); iconic != 0 {
		procShowWindow.Call(h, swRestore)
	} else {
		procShowWindow.Call(h, swShow)
	}
	procSetForegroundWindow.Call(h)
	return true
}

// Quit destroys the window and ends its message loop.
func Quit() {
	if app != nil && app.hwnd != 0 {
		procPostMessage.Call(uintptr(app.hwnd), wmAppQuit, 0, 0)
	}
}

func (a *App) buildMenu() {
	menuBar, _, _ := procCreateMenu.Call()

	file, _, _ := procCreatePopupMenu.Call()
	procAppendMenu.Call(file, mfString, cmdAdd, uintptr(unsafe.Pointer(utf16Ptr("&Add URL...\tIns"))))
	procAppendMenu.Call(file, mfSeparator, 0, 0)
	procAppendMenu.Call(file, mfString, cmdOpenFolder, uintptr(unsafe.Pointer(utf16Ptr("Open &downloads folder"))))
	procAppendMenu.Call(file, mfString, cmdWebUI, uintptr(unsafe.Pointer(utf16Ptr("Open &web UI"))))
	procAppendMenu.Call(file, mfSeparator, 0, 0)
	procAppendMenu.Call(file, mfString, cmdExit, uintptr(unsafe.Pointer(utf16Ptr("E&xit"))))
	procAppendMenu.Call(menuBar, mfPopup, file, uintptr(unsafe.Pointer(utf16Ptr("&File"))))

	dl, _, _ := procCreatePopupMenu.Call()
	procAppendMenu.Call(dl, mfString, cmdResume, uintptr(unsafe.Pointer(utf16Ptr("&Resume"))))
	procAppendMenu.Call(dl, mfString, cmdPause, uintptr(unsafe.Pointer(utf16Ptr("&Pause"))))
	procAppendMenu.Call(dl, mfSeparator, 0, 0)
	procAppendMenu.Call(dl, mfString, cmdOpen, uintptr(unsafe.Pointer(utf16Ptr("&Open file"))))
	procAppendMenu.Call(dl, mfString, cmdReveal, uintptr(unsafe.Pointer(utf16Ptr("Show in &Explorer"))))
	procAppendMenu.Call(dl, mfSeparator, 0, 0)
	procAppendMenu.Call(dl, mfString, cmdRemove, uintptr(unsafe.Pointer(utf16Ptr("Remove from &list\tDel"))))
	procAppendMenu.Call(dl, mfString, cmdRemoveFile, uintptr(unsafe.Pointer(utf16Ptr("Remove and &delete file"))))
	procAppendMenu.Call(dl, mfSeparator, 0, 0)
	procAppendMenu.Call(dl, mfString, cmdResumeAll, uintptr(unsafe.Pointer(utf16Ptr("Resume a&ll"))))
	procAppendMenu.Call(dl, mfString, cmdPauseAll, uintptr(unsafe.Pointer(utf16Ptr("Pause &all"))))
	procAppendMenu.Call(dl, mfString, cmdStopAll, uintptr(unsafe.Pointer(utf16Ptr("&Stop all"))))
	procAppendMenu.Call(menuBar, mfPopup, dl, uintptr(unsafe.Pointer(utf16Ptr("&Downloads"))))

	// Options carries the switches that change how DM behaves, checked to
	// reflect the current setting.
	opt, _, _ := procCreatePopupMenu.Call()
	a.optionsMenu = syscall.Handle(opt)
	procAppendMenu.Call(opt, mfString, cmdStartWithWindows,
		uintptr(unsafe.Pointer(utf16Ptr("Start DM with &Windows"))))
	procAppendMenu.Call(opt, mfSeparator, 0, 0)
	procAppendMenu.Call(opt, mfString, cmdShowStartDialog,
		uintptr(unsafe.Pointer(utf16Ptr("Ask where to save each &download"))))
	procAppendMenu.Call(opt, mfString, cmdShowCompleteDialog,
		uintptr(unsafe.Pointer(utf16Ptr("Show the download &complete dialog"))))
	procAppendMenu.Call(opt, mfSeparator, 0, 0)
	procAppendMenu.Call(opt, mfString, cmdWebUI,
		uintptr(unsafe.Pointer(utf16Ptr("More settings (&web UI)..."))))
	procAppendMenu.Call(menuBar, mfPopup, opt, uintptr(unsafe.Pointer(utf16Ptr("&Options"))))

	help, _, _ := procCreatePopupMenu.Call()
	procAppendMenu.Call(help, mfString, cmdAbout, uintptr(unsafe.Pointer(utf16Ptr("&About DM"))))
	procAppendMenu.Call(menuBar, mfPopup, help, uintptr(unsafe.Pointer(utf16Ptr("&Help"))))

	procSetMenu.Call(uintptr(a.hwnd), menuBar)
}

// toolbarButton is one of the buttons across the top of the window.
type toolbarButton struct {
	label string
	cmd   uintptr
	width int32
}

var toolbarButtons = []toolbarButton{
	{"Add URL", cmdAdd, 84},
	{"Resume", cmdResume, 74},
	{"Pause", cmdPause, 66},
	{"Remove", cmdRemove, 74},
	{"Open", cmdOpen, 62},
	{"Folder", cmdReveal, 66},
	{"Resume All", cmdResumeAll, 86},
	{"Pause All", cmdPauseAll, 78},
	{"Stop All", cmdStopAll, 74},
}

const toolbarHeight = 38

func (a *App) buildChildren(inst uintptr) {
	x := int32(8)
	for _, b := range toolbarButtons {
		h, _, _ := procCreateWindowEx.Call(
			0,
			uintptr(unsafe.Pointer(utf16Ptr("BUTTON"))),
			uintptr(unsafe.Pointer(utf16Ptr(b.label))),
			wsChild|wsVisible|wsTabStop|bsPushButton,
			uintptr(x), 7, uintptr(b.width), 25,
			uintptr(a.hwnd), b.cmd, inst, 0,
		)
		if h != 0 {
			procSendMessage.Call(h, wmSetFont, uintptr(a.font), 1)
			a.buttons = append(a.buttons, syscall.Handle(h))
		}
		x += b.width + 6
	}

	lv, _, _ := procCreateWindowEx.Call(
		wsExClientEdge,
		uintptr(unsafe.Pointer(utf16Ptr("SysListView32"))),
		0,
		wsChild|wsVisible|wsTabStop|lvsReport|lvsShowSelAlways,
		0, toolbarHeight, 100, 100,
		uintptr(a.hwnd), idListView, inst, 0,
	)
	a.list = syscall.Handle(lv)
	procSendMessage.Call(lv, wmSetFont, uintptr(a.font), 1)
	procSendMessage.Call(lv, lvmSetExtendedLVS, 0,
		lvsExFullRowSelect|lvsExDoubleBuffer|lvsExHeaderDragDrop)

	for i, c := range columns {
		col := lvColumn{
			Mask:     lvcfText | lvcfWidth | lvcfSubItem | lvcfFmt,
			Fmt:      c.align,
			Cx:       c.width,
			PszText:  utf16Ptr(c.title),
			ISubItem: int32(i),
		}
		procSendMessage.Call(lv, lvmInsertColumnW, uintptr(i), uintptr(unsafe.Pointer(&col)))
	}
	a.layout()
}

func (a *App) layout() {
	var r rect
	procGetClientRect.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(&r)))
	procMoveWindow.Call(uintptr(a.list), 0, toolbarHeight,
		uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top-toolbarHeight), 1)
}

// --- data ------------------------------------------------------------------

// refresh pulls state from the daemon and repaints the list.
//
// The whole list is rewritten each tick rather than diffed: a download list
// is a handful of rows, and correctness matters more than saving redraws.
func (a *App) refresh() {
	st, err := a.client.State()
	if err != nil {
		a.mu.Lock()
		a.pending, a.dirty = nil, true
		a.mu.Unlock()
		procPostMessage.Call(uintptr(a.hwnd), wmAppRefresh, 0, 0)
		procSetWindowText.Call(uintptr(a.hwnd),
			uintptr(unsafe.Pointer(utf16Ptr("DM Download Manager - daemon not reachable"))))
		return
	}

	a.mu.Lock()
	a.cfg = st.Config
	a.mu.Unlock()

	var confirms []store.Record
	var finished []store.Record

	rows := make([]row, 0, len(st.Downloads))
	var active int
	for _, r := range st.Downloads {
		if r.State == manager.StateConfirm {
			confirms = append(confirms, r)
		}
		// A completion is announced once. Records already finished when the
		// window opened are marked seen without a dialog, so starting the
		// app does not replay every past download.
		if r.State == "done" {
			a.mu.Lock()
			seen := a.seenDone[r.ID]
			a.seenDone[r.ID] = true
			a.mu.Unlock()
			if !seen && a.started && st.Config.ShowCompleteDialog {
				finished = append(finished, r)
			}
		}
		rw := row{
			ID: r.ID, Name: r.Filename, Size: r.Size, Done: r.Downloaded,
			State: r.State, Path: r.Path, Created: r.Created,
		}
		if rw.Name == "" {
			rw.Name = shortURL(r.URL)
		}
		if r.Size > 0 {
			rw.Pct = float64(r.Downloaded) / float64(r.Size) * 100
		}
		if r.State == "done" {
			rw.Pct = 100
		}
		if r.State == "downloading" || r.State == "probing" {
			active++
			// Live speed and ETA only exist for a running download.
			if p, err := a.client.Progress(r.ID); err == nil {
				if v, ok := p["speedBps"].(float64); ok {
					rw.SpeedBPS = v
				}
				if v, ok := p["eta"].(float64); ok && v > 0 {
					rw.ETA = humanDuration(v / 1e9)
				}
				if segTotal, ok := p["segmentsTotal"].(float64); ok && segTotal > 0 {
					if segDone, ok := p["segmentsDone"].(float64); ok {
						rw.Pct = segDone / segTotal * 100
					}
				}
			}
		}
		rows = append(rows, rw)
	}
	// Newest first, matching the web UI. Sorting by id would be stable but
	// meaningless, since ids are random.
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].Created.Equal(rows[j].Created) {
			return rows[i].Created.After(rows[j].Created)
		}
		return rows[i].ID < rows[j].ID
	})

	// refresh runs on a worker goroutine, and list view controls belong to
	// the thread that created them. Hand the rows over and let the window
	// procedure apply them.
	a.mu.Lock()
	a.pending = rows
	a.dirty = true
	a.mu.Unlock()
	procPostMessage.Call(uintptr(a.hwnd), wmAppRefresh, 0, 0)

	// Dialogs are modal and belong to the UI thread, so they are queued
	// here and raised from the window procedure.
	if len(confirms) > 0 || len(finished) > 0 {
		a.mu.Lock()
		a.pendingConfirm = append(a.pendingConfirm, confirms...)
		a.pendingComplete = append(a.pendingComplete, finished...)
		a.mu.Unlock()
		procPostMessage.Call(uintptr(a.hwnd), wmAppDialog, 0, 0)
	}

	a.syncOptionsMenu(st)

	title := "DM Download Manager"
	if active > 0 {
		title = fmt.Sprintf("DM Download Manager - %d active", active)
	}
	if st.Config.LimitKBps > 0 {
		title += fmt.Sprintf(" - limit %d KiB/s", st.Config.LimitKBps)
	}
	procSetWindowText.Call(uintptr(a.hwnd), uintptr(unsafe.Pointer(utf16Ptr(title))))
}

// syncOptionsMenu ticks the Options entries to match the live settings.
func (a *App) syncOptionsMenu(st *client.State) {
	if a.optionsMenu == 0 {
		return
	}
	check := func(id uintptr, on bool) {
		flag := uintptr(mfUnchecked)
		if on {
			flag = mfChecked
		}
		procCheckMenuItem.Call(uintptr(a.optionsMenu), id, mfByCommand|flag)
	}
	check(cmdStartWithWindows, st.StartWithWindows)
	check(cmdShowStartDialog, st.Config.ShowStartDialog)
	check(cmdShowCompleteDialog, st.Config.ShowCompleteDialog)
	if !st.StartWithWindowsSupported {
		procEnableMenuItem.Call(uintptr(a.optionsMenu), cmdStartWithWindows,
			mfByCommand|mfGrayed)
	}
}

// applyPending redraws the list from whatever the last refresh fetched. It
// must only be called on the UI thread.
func (a *App) applyPending() {
	a.mu.Lock()
	if !a.dirty {
		a.mu.Unlock()
		return
	}
	rows := a.pending
	a.dirty = false
	a.mu.Unlock()
	a.setRows(rows)
}

func (a *App) setRows(rows []row) {
	a.mu.Lock()
	same := len(rows) == len(a.rows)
	if same {
		for i := range rows {
			if rows[i].ID != a.rows[i].ID {
				same = false
				break
			}
		}
	}
	a.rows = rows
	a.mu.Unlock()

	if !same {
		// The set of downloads changed, so rebuild the rows outright.
		procSendMessage.Call(uintptr(a.list), lvmDeleteAllItems, 0, 0)
		for i := range rows {
			it := lvItem{Mask: lvifText, IItem: int32(i), PszText: utf16Ptr("")}
			procSendMessage.Call(uintptr(a.list), lvmInsertItemW, 0, uintptr(unsafe.Pointer(&it)))
		}
	}
	for i, r := range rows {
		a.setCell(i, 0, r.Name)
		a.setCell(i, 1, humanBytes(r.Size))
		a.setCell(i, 2, fmt.Sprintf("%.0f%%", r.Pct))
		if r.SpeedBPS > 0 {
			a.setCell(i, 3, humanBytes(int64(r.SpeedBPS))+"/s")
		} else {
			a.setCell(i, 3, "")
		}
		a.setCell(i, 4, r.State)
		a.setCell(i, 5, r.ETA)
	}
	procInvalidateRect.Call(uintptr(a.list), 0, 0)
}

func (a *App) setCell(item, sub int, text string) {
	it := lvItem{
		Mask:     lvifText,
		IItem:    int32(item),
		ISubItem: int32(sub),
		PszText:  utf16Ptr(text),
	}
	procSendMessage.Call(uintptr(a.list), lvmSetItemTextW, uintptr(item),
		uintptr(unsafe.Pointer(&it)))
}

// selected returns the highlighted rows.
func (a *App) selected() []row {
	var out []row
	a.mu.Lock()
	rows := append([]row(nil), a.rows...)
	a.mu.Unlock()

	idx := -1
	for {
		r, _, _ := procSendMessage.Call(uintptr(a.list), lvmGetNextItem,
			uintptr(int32(idx)), lvniSelected)
		i := int32(r)
		if i < 0 {
			break
		}
		idx = int(i)
		if idx >= 0 && idx < len(rows) {
			out = append(out, rows[idx])
		}
	}
	return out
}

// --- helpers ---------------------------------------------------------------

func humanBytes(n int64) string {
	if n < 0 {
		return ""
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

func humanDuration(seconds float64) string {
	s := int(seconds)
	if s <= 0 {
		return ""
	}
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm", s/3600, (s%3600)/60)
}

// filenameFromURL guesses a filename from a URL path.
func filenameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	base := path.Base(u.Path)
	if base == "/" || base == "." {
		return ""
	}
	if unesc, err := url.PathUnescape(base); err == nil {
		base = unesc
	}
	return base
}

func shortURL(u string) string {
	if i := strings.LastIndex(u, "/"); i >= 0 && i < len(u)-1 {
		return u[i+1:]
	}
	return u
}

func messageBox(parent syscall.Handle, title, text string, flags uintptr) uintptr {
	r, _, _ := procMessageBox.Call(uintptr(parent),
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(unsafe.Pointer(utf16Ptr(title))), flags)
	return r
}

// configDir is only used to locate the icon written by the tray package.
func loadAppIcon() syscall.Handle {
	path, err := iconPath()
	if err != nil {
		return 0
	}
	h, _, _ := procLoadImage.Call(0, uintptr(unsafe.Pointer(utf16Ptr(path))),
		1 /*IMAGE_ICON*/, 0, 0, 0x0010|0x0040 /*LR_LOADFROMFILE|LR_DEFAULTSIZE*/)
	return syscall.Handle(h)
}

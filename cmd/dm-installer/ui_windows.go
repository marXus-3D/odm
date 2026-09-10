//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// The wizard is a single window with two faces: options before the
// install, and a log afterwards. It is drawn in the same dark palette as
// DM itself, so the installer does not look like a different product.

const (
	idInstall = 100 + iota
	idCancel
	idRunAtLogin
	idDesktopIcon
	idBrowser
	idBrowse
	idOpenExtensions
	idLog
	idPath
	idRemoveData
)

type wizard struct {
	hwnd   syscall.Handle
	inst   uintptr
	font   syscall.Handle
	bold   syscall.Handle
	small  syscall.Handle
	iconFn syscall.Handle

	pathEdit   syscall.Handle
	logEdit    syscall.Handle
	checkLogin syscall.Handle
	checkIcon  syscall.Handle
	checkBrow  syscall.Handle
	checkData  syscall.Handle

	uninstall bool
	running   bool
	finished  bool
	failed    bool

	// installedDir is where the work actually happened, which may differ
	// from the default once the user has browsed for a folder.
	installedDir string

	mu  sync.Mutex
	log []string
}

var wz *wizard

// RunWizard shows the installer window. It returns when the user closes it.
func RunWizard(uninstall bool) error {
	initTheme()

	w := &wizard{inst: moduleHandle(), uninstall: uninstall}
	wz = w
	w.font = uiFont(0, false)
	w.bold = uiFont(19, true)
	w.small = uiFont(12, false)

	className := utf16Ptr("DMInstallerWindow")
	cursor, _, _ := procLoadCursor.Call(0, idcArrow)
	wc := wndClassEx{
		WndProc:    syscall.NewCallback(wizardProc),
		Instance:   syscall.Handle(w.inst),
		Cursor:     syscall.Handle(cursor),
		Background: syscall.Handle(brushSurface),
		ClassName:  className,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("register class: %w", err)
	}

	title := "Install " + appName
	if uninstall {
		title = "Uninstall " + appName
	}
	const width, height = 620, 470
	hwnd, _, err := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr(title))),
		wsCaption|wsSysMenu|wsMinimizeBox|wsVisible|wsClipChildren,
		cwUseDefault, cwUseDefault, width, height,
		0, 0, w.inst, 0)
	if hwnd == 0 {
		return fmt.Errorf("create window: %w", err)
	}
	w.hwnd = syscall.Handle(hwnd)
	allowDarkModeForWindow(w.hwnd)
	applyDarkTitleBar(w.hwnd)
	w.build()

	procShowWindow.Call(hwnd, swShowNormal)
	procUpdateWindow.Call(hwnd)

	var m msgStruct
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	return nil
}

func (w *wizard) build() {
	mk := func(class, text string, style uintptr, x, y, cw, ch int32, id uintptr, ex uintptr) syscall.Handle {
		var t uintptr
		if text != "" {
			t = uintptr(unsafe.Pointer(utf16Ptr(text)))
		}
		h, _, _ := procCreateWindowEx.Call(ex,
			uintptr(unsafe.Pointer(utf16Ptr(class))), t,
			wsChild|wsVisible|style,
			uintptr(x), uintptr(y), uintptr(cw), uintptr(ch),
			uintptr(w.hwnd), id, w.inst, 0)
		if h != 0 {
			procSendMessage.Call(h, wmSetFont, uintptr(w.font), 1)
			applyDarkControlTheme(syscall.Handle(h))
		}
		return syscall.Handle(h)
	}

	if w.uninstall {
		w.checkData = mk("BUTTON", "Also delete my settings and download list",
			bsAutoCheckBox|wsTabStop, 28, 150, 420, 22, idRemoveData, 0)
		w.logEdit = mk("EDIT", "",
			wsVScroll|esMultiline|esReadOnly|esAutoVScroll, 28, 190, 564, 176, idLog, 0)
		darkField(w.logEdit)
		procShowWindow.Call(uintptr(w.logEdit), swHide)
		makeButton(w.hwnd, w.inst, w.font, "Uninstall", 372, 392, 110, 34, idInstall, true)
		makeButton(w.hwnd, w.inst, w.font, "Close", 492, 392, 100, 34, idCancel, false)
		return
	}

	mk("STATIC", "Install location", ssLeft, 28, 118, 200, 18, 0, 0)
	w.pathEdit = mk("EDIT", DefaultDir(), esAutoHScroll|wsTabStop,
		29, 141, 468, 24, idPath, 0)
	darkField(w.pathEdit)
	makeButton(w.hwnd, w.inst, w.font, "...", 506, 139, 40, 28, idBrowse, false)

	w.checkLogin = mk("BUTTON", "Start DM when I sign in",
		bsAutoCheckBox|wsTabStop, 28, 184, 400, 22, idRunAtLogin, 0)
	w.checkIcon = mk("BUTTON", "Create a desktop shortcut",
		bsAutoCheckBox|wsTabStop, 28, 210, 400, 22, idDesktopIcon, 0)
	w.checkBrow = mk("BUTTON", "Set up the browser extension",
		bsAutoCheckBox|wsTabStop, 28, 236, 400, 22, idBrowser, 0)
	for _, c := range []syscall.Handle{w.checkLogin, w.checkIcon, w.checkBrow} {
		procSendMessage.Call(uintptr(c), bmSetCheck, bstChecked, 0)
	}

	w.logEdit = mk("EDIT", "",
		wsVScroll|esMultiline|esReadOnly|esAutoVScroll, 28, 118, 564, 250, idLog, 0)
	darkField(w.logEdit)
	procShowWindow.Call(uintptr(w.logEdit), swHide)

	makeButton(w.hwnd, w.inst, w.font, "Install", 372, 392, 110, 34, idInstall, true)
	makeButton(w.hwnd, w.inst, w.font, "Cancel", 492, 392, 100, 34, idCancel, false)
}

// say appends a line to the log pane.
func (w *wizard) say(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	w.mu.Lock()
	w.log = append(w.log, line)
	text := strings.Join(w.log, "\r\n")
	w.mu.Unlock()

	procSetWindowText.Call(uintptr(w.logEdit), uintptr(unsafe.Pointer(utf16Ptr(text))))
	// Keep the newest line in view.
	const emSetSel, emScrollCaret = 0x00B1, 0x00B7
	procSendMessage.Call(uintptr(w.logEdit), emSetSel, ^uintptr(0), ^uintptr(0))
	procSendMessage.Call(uintptr(w.logEdit), emScrollCaret, 0, 0)
}

func (w *wizard) checked(h syscall.Handle) bool {
	if h == 0 {
		return false
	}
	r, _, _ := procSendMessage.Call(uintptr(h), bmGetCheck, 0, 0)
	return r == bstChecked
}

// start runs the install or uninstall on a worker, so the window keeps
// painting while files are copied.
func (w *wizard) start() {
	if w.running || w.finished {
		if w.finished {
			w.finishAction()
		}
		return
	}
	w.running = true

	dir := DefaultDir()
	if w.uninstall {
		dir = FindInstallDir()
	} else if t := windowText(w.pathEdit); t != "" {
		dir = t
	}

	opts := Options{
		Dir:          dir,
		RunAtLogin:   w.checked(w.checkLogin),
		DesktopIcon:  w.checked(w.checkIcon),
		SetUpBrowser: w.checked(w.checkBrow),
	}
	removeData := w.checked(w.checkData)

	// Switch to the log view.
	for _, h := range []syscall.Handle{w.pathEdit, w.checkLogin, w.checkIcon,
		w.checkBrow, w.checkData} {
		if h != 0 {
			procShowWindow.Call(uintptr(h), swHide)
		}
	}
	hideButton(idBrowse)
	procShowWindow.Call(uintptr(w.logEdit), swShow)
	setButtonLabel(idInstall, "Please wait")
	enableButton(idInstall, false)
	procInvalidateRect.Call(uintptr(w.hwnd), 0, 1)

	go func() {
		var err error
		if w.uninstall {
			err = Uninstall(dir, removeData, w.say)
		} else {
			err = Install(opts, w.say)
		}
		if err != nil {
			w.say("")
			w.say("Failed: %v", err)
		}
		w.running = false
		w.finished = true
		w.installedDir = dir
		w.failed = err != nil
		procPostMessage.Call(uintptr(w.hwnd), wmAppDone, 0, 0)
	}()
}

// finishAction is what the primary button does once the work is over.
func (w *wizard) finishAction() {
	if !w.failed && !w.uninstall {
		exe := filepath.Join(w.installedDir, "bin", "dmd.exe")
		cmd := exec.Command(exe)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		cmd.Start()
	}
	procPostMessage.Call(uintptr(w.hwnd), wmClose, 0, 0)
}

func (w *wizard) onDone() {
	if w.uninstall {
		setButtonLabel(idInstall, "Close")
	} else if w.failed {
		setButtonLabel(idInstall, "Close")
	} else {
		setButtonLabel(idInstall, "Finish")
		if w.checked(w.checkBrow) {
			// The button only appears when there is something to open.
			makeButton(w.hwnd, w.inst, w.font, "Load the extension",
				28, 392, 170, 34, idOpenExtensions, false)
		}
	}
	enableButton(idInstall, true)
	setButtonLabel(idCancel, "Close")
	procInvalidateRect.Call(uintptr(w.hwnd), 0, 1)
}

// paint draws the header and the page chrome.
func (w *wizard) paint() {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&ps)))

	var cr rect
	procGetClientRect.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&cr)))
	dc := syscall.Handle(hdc)
	fillRect(dc, cr, colSurface)

	// Header band.
	head := rect{0, 0, cr.Right, 96}
	fillRect(dc, head, colBackground)
	fillRect(dc, rect{0, 95, cr.Right, 96}, colBorder)

	title := appName
	sub := "Parallel downloads, resume, HLS video and browser integration."
	if w.uninstall {
		sub = "This will remove DM from your computer."
	}
	drawText(dc, title, rect{28, 24, cr.Right - 28, 52}, colText, w.bold,
		dtLeft|dtVCenter|dtSingleLine)
	drawText(dc, sub, rect{28, 54, cr.Right - 28, 82}, colTextDim, w.small,
		dtLeft|dtVCenter|dtSingleLine)
	drawText(dc, "v"+Version, rect{cr.Right - 120, 24, cr.Right - 28, 52},
		colTextDim, w.small, dtRight|dtVCenter|dtSingleLine)

	// A one pixel frame behind the path field: the edit is unthemed so it
	// has no border of its own, and the children clip over this.
	if !w.uninstall && !w.running && !w.finished {
		fillRect(dc, rect{28, 140, 498, 166}, colBorder)
		fillRect(dc, rect{29, 141, 497, 165}, colField)
	}

	// Footer separator above the buttons.
	fillRect(dc, rect{0, 378, cr.Right, 379}, colBorder)
}

func wizardProc(hwnd syscall.Handle, msg uint32, wparam, lparam uintptr) uintptr {
	w := wz
	switch msg {
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		if w != nil {
			w.paint()
			return 0
		}
	case wmDrawItem:
		if w != nil && drawDialogButton((*drawItemStruct)(lparamPtr(lparam)), w.font) {
			return 1
		}
	case wmCtlColorStatic, wmCtlColorBtn, wmCtlColorDlg,
		wmCtlColorEdit, wmCtlColorListBox:
		if brush, ok := darkCtlColor(msg, wparam); ok {
			return brush
		}
	case wmAppDone:
		if w != nil {
			w.onDone()
		}
		return 0
	case wmCommand:
		if w == nil {
			break
		}
		switch uint32(wparam & 0xFFFF) {
		case idInstall:
			w.start()
			return 0
		case idCancel:
			procPostMessage.Call(uintptr(hwnd), wmClose, 0, 0)
			return 0
		case idBrowse:
			if d := pickFolder(w.hwnd, windowText(w.pathEdit)); d != "" {
				procSetWindowText.Call(uintptr(w.pathEdit),
					uintptr(unsafe.Pointer(utf16Ptr(d))))
			}
			return 0
		case idOpenExtensions:
			OpenExtensionsPage(w.installedDir)
			messageBox(w.hwnd, "Load the extension",
				"The extensions page is open and the folder is on your clipboard.\n\n"+
					"1.  Turn on Developer mode\n"+
					"2.  Choose \"Load unpacked\"\n"+
					"3.  Paste the path and confirm",
				mbOk|mbIconInfo)
			return 0
		}
	case wmClose:
		procDestroyWindow.Call(uintptr(hwnd))
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return r
}

// pickFolder shows the folder browser.
func pickFolder(owner syscall.Handle, current string) string {
	var bi browseInfo
	bi.Owner = owner
	bi.Title = utf16Ptr("Choose where to install DM")
	bi.Flags = bifReturnOnlyFsDirs | bifNewDialogStyle

	pidl, _, _ := procSHBrowseForFolder.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	buf := make([]uint16, 520)
	procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
	picked := syscall.UTF16ToString(buf)
	if picked == "" {
		return ""
	}
	// The browser returns a parent folder; keep DM as the leaf.
	if filepath.Base(picked) != "DM" {
		picked = filepath.Join(picked, "DM")
	}
	return picked
}

func moduleHandle() uintptr {
	h, _, _ := procGetModuleHandle.Call(0)
	return h
}

var _ = os.Getenv

//go:build windows

package nativeui

import (
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/marXus-3D/odm/internal/client"
	"github.com/marXus-3D/odm/internal/store"
)

// Control ids for the Download File Info form.
const (
	idFIUrl = 4001 + iota
	idFICategory
	idFISaveAs
	idFIBrowse
	idFIRemember
	idFIDescription
	idFILater
	idFIStart
	idFICancel
)

// fileInfo is the "Download File Info" form: where a download is going,
// which category it belongs to, and whether to start it now. It is the
// dialog IDM shows when you click a link.
type fileInfo struct {
	hwnd     syscall.Handle
	url      syscall.Handle
	category syscall.Handle
	saveAs   syscall.Handle
	remember syscall.Handle
	desc     syscall.Handle

	rec        store.Record
	categories []store.Category
	cfg        store.Config

	done      bool
	answered  bool
	answer    client.Confirmation
	remember2 bool
}

var fileInfoDlg *fileInfo

// showFileInfo asks where a download should go. It returns false when the
// user cancelled, in which case the caller removes the download.
func (a *App) showFileInfo(rec store.Record, cfg store.Config) (client.Confirmation, bool, bool) {
	inst, _, _ := procGetModuleHandle.Call(0)
	className := dialogClass("DMFileInfo", syscall.NewCallback(fileInfoProc), inst)

	d := &fileInfo{rec: rec, cfg: cfg, categories: cfg.Categories}
	fileInfoDlg = d
	defer func() { fileInfoDlg = nil }()

	const w, h = 600, 300
	hwnd, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("Download File Info"))),
		wsCaption|wsSysMenu|wsVisible,
		cwUseDefault, cwUseDefault, w, h,
		uintptr(a.hwnd), 0, inst, 0)
	if hwnd == 0 {
		return client.Confirmation{}, false, false
	}
	d.hwnd = syscall.Handle(hwnd)
	applyDarkTitleBar(d.hwnd)

	mk := func(class, text string, style uintptr, x, y, cw, ch int32, id uintptr, exStyle uintptr) syscall.Handle {
		var t uintptr
		if text != "" {
			t = uintptr(unsafe.Pointer(utf16Ptr(text)))
		}
		h, _, _ := procCreateWindowEx.Call(exStyle,
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

	const labelW, fieldX, fieldW = 78, 92, 470

	mk("STATIC", "URL:", ssLeft, 12, 18, labelW, 18, 0, 0)
	d.url = mk("EDIT", rec.URL, wsTabStop|esAutoHScroll|esReadOnly,
		fieldX, 15, fieldW, 23, idFIUrl, wsExClientEdge)

	mk("STATIC", "Category:", ssLeft, 12, 54, labelW, 18, 0, 0)
	d.category = mk("COMBOBOX", "", wsTabStop|cbsDropDownList|wsVScroll,
		fieldX, 50, 220, 220, idFICategory, 0)
	sel := 0
	for i, c := range d.categories {
		procSendMessage.Call(uintptr(d.category), cbAddString, 0,
			uintptr(unsafe.Pointer(utf16Ptr(c.Name))))
		if c.Name == rec.Category {
			sel = i
		}
	}
	procSendMessage.Call(uintptr(d.category), cbSetCurSel, uintptr(sel), 0)

	name := rec.Filename
	if name == "" {
		name = filenameFromURL(rec.URL)
	}
	dir := rec.Dir
	if dir == "" {
		dir = cfg.DirFor(cfg.CategoryByName(rec.Category))
	}

	mk("STATIC", "Save As:", ssLeft, 12, 90, labelW, 18, 0, 0)
	d.saveAs = mk("EDIT", filepath.Join(dir, name), wsTabStop|esAutoHScroll,
		fieldX, 87, fieldW-40, 23, idFISaveAs, wsExClientEdge)
	makeButton(syscall.Handle(hwnd), inst, a.font, "...",
		fieldX+fieldW-34, 86, 34, 25, idFIBrowse, false)

	d.remember = mk("BUTTON", "Remember this path for the \""+rec.Category+"\" category",
		wsTabStop|bsAutoCheckBox, fieldX, 120, 400, 20, idFIRemember, 0)

	mk("STATIC", "Description:", ssLeft, 12, 154, labelW, 18, 0, 0)
	d.desc = mk("EDIT", rec.Description, wsTabStop|esAutoHScroll,
		fieldX, 151, fieldW, 23, idFIDescription, wsExClientEdge)

	makeButton(syscall.Handle(hwnd), inst, a.font, "Download Later",
		190, 200, 120, 28, idFILater, false)
	makeButton(syscall.Handle(hwnd), inst, a.font, "Start Download",
		320, 200, 120, 28, idFIStart, true)
	makeButton(syscall.Handle(hwnd), inst, a.font, "Cancel",
		450, 200, 100, 28, idFICancel, false)

	procEnableWindow.Call(uintptr(a.hwnd), 0)
	procSetFocus.Call(uintptr(d.saveAs))

	var m msgStruct
	for !d.done {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		if m.Message == 0x0100 { // WM_KEYDOWN
			switch m.WParam {
			case 0x0D:
				d.finish(true, true)
				continue
			case 0x1B:
				d.finish(false, false)
				continue
			}
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}

	procEnableWindow.Call(uintptr(a.hwnd), 1)
	procSetForegroundWindow.Call(uintptr(a.hwnd))
	return d.answer, d.answered, d.remember2
}

// finish records the answer and closes the form. accept false means cancel;
// start says whether the download begins now or waits.
func (d *fileInfo) finish(accept, start bool) {
	if accept {
		full := windowText(d.saveAs)
		d.answer = client.Confirmation{
			Dir:         filepath.Dir(full),
			Filename:    filepath.Base(full),
			Category:    d.selectedCategory(),
			Description: windowText(d.desc),
			Start:       start,
		}
		d.answered = true
		checked, _, _ := procSendMessage.Call(uintptr(d.remember), bmGetCheck, 0, 0)
		d.remember2 = checked == bstChecked
	}
	d.done = true
	if d.hwnd != 0 {
		procDestroyWindow.Call(uintptr(d.hwnd))
		d.hwnd = 0
	}
}

func (d *fileInfo) selectedCategory() string {
	i, _, _ := procSendMessage.Call(uintptr(d.category), cbGetCurSel, 0, 0)
	idx := int(int32(i))
	if idx >= 0 && idx < len(d.categories) {
		return d.categories[idx].Name
	}
	return d.rec.Category
}

// browse opens the standard save-as dialog, seeded with the current path.
func (d *fileInfo) browse() {
	current := windowText(d.saveAs)
	picked, ok := saveFileDialog(d.hwnd, "Save download as", current)
	if !ok {
		return
	}
	procSetWindowText.Call(uintptr(d.saveAs), uintptr(unsafe.Pointer(utf16Ptr(picked))))
}

// categoryChanged repoints Save As at the new category's folder, keeping
// whatever filename is already there.
func (d *fileInfo) categoryChanged() {
	cat := d.cfg.CategoryByName(d.selectedCategory())
	current := windowText(d.saveAs)
	name := filepath.Base(current)
	if name == "." || name == string(filepath.Separator) {
		name = filenameFromURL(d.rec.URL)
	}
	procSetWindowText.Call(uintptr(d.saveAs),
		uintptr(unsafe.Pointer(utf16Ptr(filepath.Join(d.cfg.DirFor(cat), name)))))
	procSetWindowText.Call(uintptr(d.remember),
		uintptr(unsafe.Pointer(utf16Ptr(
			"Remember this path for the \""+cat.Name+"\" category"))))
}

func fileInfoProc(hwnd syscall.Handle, message uint32, wparam, lparam uintptr) uintptr {
	d := fileInfoDlg
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
		id := uint32(wparam & 0xFFFF)
		notify := uint32(wparam >> 16)
		switch id {
		case idFIStart:
			d.finish(true, true)
			return 0
		case idFILater:
			d.finish(true, false)
			return 0
		case idFICancel:
			d.finish(false, false)
			return 0
		case idFIBrowse:
			d.browse()
			return 0
		case idFICategory:
			const cbnSelChange = 1
			if notify == cbnSelChange {
				d.categoryChanged()
			}
			return 0
		}
	case wmClose:
		if d != nil {
			d.finish(false, false)
		}
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(message), wparam, lparam)
	return ret
}

// saveFileDialog shows the standard Windows save-as browser.
func saveFileDialog(owner syscall.Handle, title, initial string) (string, bool) {
	buf := make([]uint16, 1024)
	if initial != "" {
		src := syscall.StringToUTF16(filepath.Base(initial))
		copy(buf, src)
	}

	var ofn openFileName
	ofn.StructSize = uint32(unsafe.Sizeof(ofn))
	ofn.Owner = owner
	// "All files" only: the name already carries the right extension, and a
	// filter would fight the user over it.
	ofn.Filter = &utf16Filter("All files\x00*.*\x00")[0]
	ofn.File = &buf[0]
	ofn.MaxFile = uint32(len(buf))
	ofn.Title = utf16Ptr(title)
	ofn.Flags = ofnOverwritePrompt | ofnPathMustExist | ofnHideReadOnly |
		ofnExplorer | ofnNoChangeDir
	if dir := filepath.Dir(initial); dir != "" && dir != "." {
		ofn.InitialDir = utf16Ptr(dir)
	}

	ret, _, _ := procGetSaveFileName.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		return "", false // cancelled
	}
	return syscall.UTF16ToString(buf), true
}

// utf16Filter encodes a double-NUL terminated filter string.
func utf16Filter(s string) []uint16 {
	out := make([]uint16, 0, len(s)+1)
	for _, r := range s {
		out = append(out, uint16(r))
	}
	out = append(out, 0)
	return out
}

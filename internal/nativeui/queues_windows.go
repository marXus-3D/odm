//go:build windows

package nativeui

import (
	"fmt"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/marXus-3D/odm/internal/client"
)

// The Queues form: the named lines downloads wait in, and how many of each
// may run at once. One queue for a game on a single connection, another for
// videos three at a time.

const (
	idQList = 4301 + iota
	idQName
	idQMax
	idQAdd
	idQApply
	idQRemove
	idQClose
)

type queuesDlg struct {
	hwnd syscall.Handle
	list syscall.Handle
	name syscall.Handle
	max  syscall.Handle

	queues []client.QueueStatus
	done   bool
}

var queuesForm *queuesDlg

// showQueues runs the queue manager. It is modal, like the other forms.
func (a *App) showQueues() {
	inst, _, _ := procGetModuleHandle.Call(0)
	className := dialogClass("DMQueues", syscall.NewCallback(queuesProc), inst)

	d := &queuesDlg{}
	queuesForm = d
	defer func() { queuesForm = nil }()

	const w, h = 470, 330
	hwnd, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("Download queues"))),
		wsCaption|wsSysMenu|wsVisible,
		cwUseDefault, cwUseDefault, w, h,
		uintptr(a.hwnd), 0, inst, 0)
	if hwnd == 0 {
		return
	}
	d.hwnd = syscall.Handle(hwnd)
	applyDarkTitleBar(d.hwnd)

	mk := func(class, text string, style uintptr, x, y, cw, ch int32, id uintptr, ex uintptr) syscall.Handle {
		var t uintptr
		if text != "" {
			t = uintptr(unsafe.Pointer(utf16Ptr(text)))
		}
		hh, _, _ := procCreateWindowEx.Call(ex,
			uintptr(unsafe.Pointer(utf16Ptr(class))), t,
			wsChild|wsVisible|style,
			uintptr(x), uintptr(y), uintptr(cw), uintptr(ch),
			hwnd, id, inst, 0)
		if hh != 0 {
			procSendMessage.Call(hh, wmSetFont, uintptr(a.font), 1)
			applyDarkControlTheme(syscall.Handle(hh))
		}
		return syscall.Handle(hh)
	}

	mk("STATIC", "Queues, and how many of each run at once:", ssLeft, 16, 12, 420, 18, 0, 0)
	d.list = mk("LISTBOX", "", wsTabStop|wsVScroll|lbsNotify, 16, 36, 420, 150, idQList, wsExClientEdge)
	darkField(d.list)

	mk("STATIC", "Name:", ssLeft, 16, 200, 44, 18, 0, 0)
	d.name = mk("EDIT", "", wsTabStop|esAutoHScroll, 62, 197, 200, 23, idQName, wsExClientEdge)
	darkField(d.name)

	mk("STATIC", "At once:", ssLeft, 276, 200, 60, 18, 0, 0)
	d.max = mk("EDIT", "", wsTabStop|esAutoHScroll|esNumber, 340, 197, 50, 23, idQMax, wsExClientEdge)
	darkField(d.max)

	makeButton(d.hwnd, inst, a.font, "Add", 16, 234, 90, 28, idQAdd, false)
	makeButton(d.hwnd, inst, a.font, "Apply", 112, 234, 90, 28, idQApply, true)
	makeButton(d.hwnd, inst, a.font, "Remove", 208, 234, 90, 28, idQRemove, false)
	makeButton(d.hwnd, inst, a.font, "Close", 346, 234, 90, 28, idQClose, false)

	d.reload()
	procEnableWindow.Call(uintptr(a.hwnd), 0)
	procSetFocus.Call(uintptr(d.list))

	var m msgStruct
	for !d.done {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		if m.Message == 0x0100 && m.WParam == 0x1B { // Esc
			d.finish()
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}

	procEnableWindow.Call(uintptr(a.hwnd), 1)
	procSetForegroundWindow.Call(uintptr(a.hwnd))
	a.refresh()
}

// reload refills the list from the daemon.
func (d *queuesDlg) reload() {
	a := app
	if a == nil {
		return
	}
	queues, err := a.client.Queues()
	if err != nil {
		messageBox(d.hwnd, "Queues", "Could not read the queues:\n\n"+err.Error(),
			mbOk|mbIconError)
		return
	}
	d.queues = queues

	procSendMessage.Call(uintptr(d.list), lbResetContent, 0, 0)
	for _, q := range queues {
		// Separators rather than space padding: the list box uses the UI
		// font, so padded columns come out ragged.
		line := fmt.Sprintf("%s  -  %d at once  -  %d running, %d waiting",
			q.Name, q.MaxConcurrent, q.Running, q.Waiting)
		procSendMessage.Call(uintptr(d.list), lbAddString, 0,
			uintptr(unsafe.Pointer(utf16Ptr(line))))
	}
}

// selected returns the highlighted queue.
func (d *queuesDlg) selected() (client.QueueStatus, bool) {
	i, _, _ := procSendMessage.Call(uintptr(d.list), lbGetCurSel, 0, 0)
	idx := int(int32(i))
	if idx < 0 || idx >= len(d.queues) {
		return client.QueueStatus{}, false
	}
	return d.queues[idx], true
}

// fillFields copies the highlighted queue into the edit boxes.
func (d *queuesDlg) fillFields() {
	q, ok := d.selected()
	if !ok {
		return
	}
	procSetWindowText.Call(uintptr(d.name), uintptr(unsafe.Pointer(utf16Ptr(q.Name))))
	procSetWindowText.Call(uintptr(d.max),
		uintptr(unsafe.Pointer(utf16Ptr(strconv.Itoa(q.MaxConcurrent)))))
}

func (d *queuesDlg) fields() (string, int) {
	name := windowText(d.name)
	n, _ := strconv.Atoi(windowText(d.max))
	return name, n
}

func (d *queuesDlg) add() {
	a := app
	name, n := d.fields()
	if name == "" {
		messageBox(d.hwnd, "Queues", "Give the queue a name first.", mbOk|mbIconInfo)
		return
	}
	if n < 1 {
		n = 1
	}
	if _, err := a.client.AddQueue(name, n); err != nil {
		messageBox(d.hwnd, "Queues", err.Error(), mbOk|mbIconError)
		return
	}
	d.reload()
}

func (d *queuesDlg) apply() {
	a := app
	q, ok := d.selected()
	if !ok {
		messageBox(d.hwnd, "Queues", "Pick a queue from the list first.", mbOk|mbIconInfo)
		return
	}
	name, n := d.fields()
	if _, err := a.client.UpdateQueue(q.ID, name, n); err != nil {
		messageBox(d.hwnd, "Queues", err.Error(), mbOk|mbIconError)
		return
	}
	d.reload()
}

func (d *queuesDlg) remove() {
	a := app
	q, ok := d.selected()
	if !ok {
		return
	}
	msg := fmt.Sprintf("Remove the %q queue?\n\n"+
		"Its downloads move to the default queue; nothing is deleted.", q.Name)
	if messageBox(d.hwnd, "Queues", msg, mbOkCancel|mbIconQuestion) != idOK {
		return
	}
	if err := a.client.RemoveQueue(q.ID); err != nil {
		messageBox(d.hwnd, "Queues", err.Error(), mbOk|mbIconError)
		return
	}
	d.reload()
}

func (d *queuesDlg) finish() {
	d.done = true
	if d.hwnd != 0 {
		procDestroyWindow.Call(uintptr(d.hwnd))
		d.hwnd = 0
	}
}

func queuesProc(hwnd syscall.Handle, message uint32, wparam, lparam uintptr) uintptr {
	d := queuesForm
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
		case idQList:
			if uint32(wparam>>16) == lbnSelChange {
				d.fillFields()
			}
			return 0
		case idQAdd:
			d.add()
			return 0
		case idQApply:
			d.apply()
			return 0
		case idQRemove:
			d.remove()
			return 0
		case idQClose:
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

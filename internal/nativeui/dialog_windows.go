//go:build windows

package nativeui

import (
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"github.com/marcus/dm/internal/client"
)

const (
	idEdit   = 3001
	idOK     = 3002
	idCancel = 3003
)

// addDialog is the "Add URL" window. It is a plain child-window form rather
// than a dialog resource, because dialog templates need a resource compiler.
type addDialog struct {
	hwnd   syscall.Handle
	edit   syscall.Handle
	result string
	done   bool
}

var dlg *addDialog

// promptAdd asks for a URL and queues it.
func (a *App) promptAdd() {
	url := a.askURL()
	if url == "" {
		return
	}
	for _, u := range strings.Fields(url) {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			messageBox(a.hwnd, "Add URL",
				"Only http and https URLs can be downloaded:\n\n"+u, mbOk|mbIconError)
			continue
		}
		if _, err := a.client.Add(client.AddRequest{URL: u}); err != nil {
			messageBox(a.hwnd, "Add URL", "Could not queue the download:\n\n"+err.Error(),
				mbOk|mbIconError)
		}
	}
	go a.refresh()
}

// askURL shows a small modal form and returns what was typed.
func (a *App) askURL() string {
	inst, _, _ := procGetModuleHandle.Call(0)
	className := utf16Ptr("DMAddDialog")

	cursor, _, _ := procLoadCursor.Call(0, idcArrow)
	bg, _, _ := procGetSysColorBrush.Call(colorBtnFace)
	wc := wndClassEx{
		WndProc:    syscall.NewCallback(dialogProc),
		Instance:   syscall.Handle(inst),
		Cursor:     syscall.Handle(cursor),
		Background: syscall.Handle(bg),
		ClassName:  className,
		Icon:       loadAppIcon(),
		IconSm:     loadAppIcon(),
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))) // fine if already registered

	d := &addDialog{}
	dlg = d

	const w, h = 520, 150
	hwnd, _, _ := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("Add URL"))),
		wsCaption|wsSysMenu|wsVisible,
		cwUseDefault, cwUseDefault, w, h,
		uintptr(a.hwnd), 0, inst, 0,
	)
	if hwnd == 0 {
		return ""
	}
	d.hwnd = syscall.Handle(hwnd)

	label, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr("STATIC"))),
		uintptr(unsafe.Pointer(utf16Ptr("Address (paste one or more URLs):"))),
		wsChild|wsVisible, 14, 14, 460, 18, hwnd, 0, inst, 0)
	procSendMessage.Call(label, wmSetFont, uintptr(a.font), 1)

	edit, _, _ := procCreateWindowEx.Call(wsExClientEdge,
		uintptr(unsafe.Pointer(utf16Ptr("EDIT"))),
		0,
		wsChild|wsVisible|wsTabStop|esAutoHScroll,
		14, 36, 484, 24, hwnd, idEdit, inst, 0)
	d.edit = syscall.Handle(edit)
	procSendMessage.Call(edit, wmSetFont, uintptr(a.font), 1)

	ok, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr("BUTTON"))),
		uintptr(unsafe.Pointer(utf16Ptr("Download"))),
		wsChild|wsVisible|wsTabStop|bsDefPushButton,
		318, 76, 85, 27, hwnd, idOK, inst, 0)
	procSendMessage.Call(ok, wmSetFont, uintptr(a.font), 1)

	cancel, _, _ := procCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr("BUTTON"))),
		uintptr(unsafe.Pointer(utf16Ptr("Cancel"))),
		wsChild|wsVisible|wsTabStop|bsPushButton,
		411, 76, 85, 27, hwnd, idCancel, inst, 0)
	procSendMessage.Call(cancel, wmSetFont, uintptr(a.font), 1)

	// Modal: disable the parent, then run a nested message loop until the
	// form closes. Simpler and more predictable than a dialog box procedure.
	procEnableWindow.Call(uintptr(a.hwnd), 0)
	procSetFocus.Call(edit)

	var m msgStruct
	for !d.done {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		// Enter and Escape are not dispatched to a plain window on their
		// own, so they are handled here.
		if m.Message == 0x0100 { // WM_KEYDOWN
			switch m.WParam {
			case 0x0D: // VK_RETURN
				d.finish(true)
				continue
			case 0x1B: // VK_ESCAPE
				d.finish(false)
				continue
			}
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}

	procEnableWindow.Call(uintptr(a.hwnd), 1)
	procSetForegroundWindow.Call(uintptr(a.hwnd))
	dlg = nil
	return d.result
}

func (d *addDialog) finish(accept bool) {
	if accept {
		d.result = windowText(d.edit)
	}
	d.done = true
	if d.hwnd != 0 {
		procDestroyWindow.Call(uintptr(d.hwnd))
		d.hwnd = 0
	}
}

func dialogProc(hwnd syscall.Handle, message uint32, wparam, lparam uintptr) uintptr {
	d := dlg
	switch message {
	case wmCommand:
		if d == nil {
			break
		}
		switch uint32(wparam & 0xFFFF) {
		case idOK:
			d.finish(true)
			return 0
		case idCancel:
			d.finish(false)
			return 0
		}
	case wmClose:
		if d != nil {
			d.finish(false)
		}
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(message), wparam, lparam)
	return ret
}

func windowText(h syscall.Handle) string {
	n, _, _ := procGetWindowTextLength.Call(uintptr(h))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowText.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

// openURL and openPath are duplicated from the daemon rather than shared,
// so this package stays independent of it.
func openURL(url string) {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}

func openPath(path string) {
	if path == "" {
		return
	}
	cmd := exec.Command("explorer.exe", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}

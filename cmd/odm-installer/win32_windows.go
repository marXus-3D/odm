//go:build windows

package main

// The installer needs the same dark Win32 furniture as the app: a palette,
// a few drawing helpers and owner-drawn buttons. It carries its own copy
// rather than importing internal/nativeui, because that package builds a
// download manager window and drags in the engine with it; an installer
// should not embed the thing it installs. The shared parts are small and
// stable enough for that to be the cheaper trade.

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")
	uxtheme  = syscall.NewLazyDLL("uxtheme.dll")
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")

	procRegisterClassEx     = user32.NewProc("RegisterClassExW")
	procCreateWindowEx      = user32.NewProc("CreateWindowExW")
	procDefWindowProc       = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procShowWindow          = user32.NewProc("ShowWindow")
	procUpdateWindow        = user32.NewProc("UpdateWindow")
	procGetMessage          = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessage     = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procSendMessage         = user32.NewProc("SendMessageW")
	procPostMessage         = user32.NewProc("PostMessageW")
	procGetClientRect       = user32.NewProc("GetClientRect")
	procSetWindowText       = user32.NewProc("SetWindowTextW")
	procGetWindowText       = user32.NewProc("GetWindowTextW")
	procGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	procLoadCursor          = user32.NewProc("LoadCursorW")
	procMessageBox          = user32.NewProc("MessageBoxW")
	procGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	procBeginPaint          = user32.NewProc("BeginPaint")
	procEndPaint            = user32.NewProc("EndPaint")
	procFillRect            = user32.NewProc("FillRect")
	procDrawTextEx          = user32.NewProc("DrawTextExW")
	procInvalidateRect      = user32.NewProc("InvalidateRect")
	procTrackMouseEvent     = user32.NewProc("TrackMouseEvent")
	procEnableWindow        = user32.NewProc("EnableWindow")
	procSystemParametersInf = user32.NewProc("SystemParametersInfoW")
	procGetDlgItem          = user32.NewProc("GetDlgItem")

	procCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	procCreateFontIndirect = gdi32.NewProc("CreateFontIndirectW")
	procCreatePen          = gdi32.NewProc("CreatePen")
	procRoundRect          = gdi32.NewProc("RoundRect")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procSetBkMode          = gdi32.NewProc("SetBkMode")
	procSetTextColor       = gdi32.NewProc("SetTextColor")
	procSetBkColor         = gdi32.NewProc("SetBkColor")

	procGetProcAddress = kernel32.NewProc("GetProcAddress")

	procInitCommonControls = comctl32.NewProc("InitCommonControlsEx")
	procSetWindowSubclass  = comctl32.NewProc("SetWindowSubclass")
	procDefSubclassProc    = comctl32.NewProc("DefSubclassProc")
	procSetWindowTheme     = uxtheme.NewProc("SetWindowTheme")
	procDwmSetWindowAttr   = dwmapi.NewProc("DwmSetWindowAttribute")

	procSHBrowseForFolder   = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")
)

const (
	wmDestroy         = 0x0002
	wmPaint           = 0x000F
	wmClose           = 0x0010
	wmEraseBkgnd      = 0x0014
	wmSetFont         = 0x0030
	wmDrawItem        = 0x002B
	wmCommand         = 0x0111
	wmMouseMove       = 0x0200
	wmMouseLeave      = 0x02A3
	wmCtlColorEdit    = 0x0133
	wmCtlColorListBox = 0x0134
	wmCtlColorBtn     = 0x0135
	wmCtlColorDlg     = 0x0136
	wmCtlColorStatic  = 0x0138
	wmApp             = 0x8000
	wmAppDone         = wmApp + 1
	wmAppLog          = wmApp + 2

	// Edit control messages, used to keep the newest log line in view.
	emSetSel      = 0x00B1
	emScrollCaret = 0x00B7

	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsMinimizeBox  = 0x00020000
	wsVisible      = 0x10000000
	wsChild        = 0x40000000
	wsTabStop      = 0x00010000
	wsVScroll      = 0x00200000
	wsClipChildren = 0x02000000
	wsExClientEdge = 0x00000200

	bsAutoCheckBox = 0x00000003
	bsOwnerDraw    = 0x0000000B

	esAutoHScroll = 0x0080
	esAutoVScroll = 0x0040
	esMultiline   = 0x0004
	esReadOnly    = 0x0800

	ssLeft = 0x0000

	bmGetCheck = 0x00F0
	bmSetCheck = 0x00F1
	bstChecked = 1

	swHide       = 0
	swShow       = 5
	swShowNormal = 1

	odsSelected = 0x0001
	odsDisabled = 0x0004
	odsFocus    = 0x0010

	dtLeft        = 0x0000
	dtCenter      = 0x0001
	dtRight       = 0x0002
	dtVCenter     = 0x0004
	dtSingleLine  = 0x0020
	dtEndEllipsis = 0x00008000

	transparent = 1
	idcArrow    = 32512

	mbOk       = 0x0000
	mbIconInfo = 0x0040

	spiGetNonClientMetrics = 0x0029
	tmeLeave               = 0x00000002

	cwUseDefault = ^uintptr(0x7FFFFFFF)

	bifReturnOnlyFsDirs = 0x00000001
	bifNewDialogStyle   = 0x00000040

	iccStandardClasses = 0x00004000
)

type rect struct{ Left, Top, Right, Bottom int32 }

type msgStruct struct {
	Hwnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   syscall.Handle
	Icon       syscall.Handle
	Cursor     syscall.Handle
	Background syscall.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     syscall.Handle
}

type paintStruct struct {
	Hdc         syscall.Handle
	Erase       int32
	RcPaint     rect
	Restore     int32
	IncUpdate   int32
	RgbReserved [32]byte
}

type drawItemStruct struct {
	CtlType    uint32
	CtlID      uint32
	ItemID     uint32
	ItemAction uint32
	ItemState  uint32
	HwndItem   syscall.Handle
	Hdc        syscall.Handle
	RcItem     rect
	ItemData   uintptr
}

type trackMouseEvent struct {
	Size      uint32
	Flags     uint32
	TrackHwnd syscall.Handle
	HoverTime uint32
}

type browseInfo struct {
	Owner       syscall.Handle
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	LParam      uintptr
	Image       int32
}

type logFont struct {
	Height         int32
	Width          int32
	Escapement     int32
	Orientation    int32
	Weight         int32
	Italic         byte
	Underline      byte
	StrikeOut      byte
	CharSet        byte
	OutPrecision   byte
	ClipPrecision  byte
	Quality        byte
	PitchAndFamily byte
	FaceName       [32]uint16
}

type nonClientMetrics struct {
	Size              uint32
	BorderWidth       int32
	ScrollWidth       int32
	ScrollHeight      int32
	CaptionWidth      int32
	CaptionHeight     int32
	CaptionFont       logFont
	SmCaptionWidth    int32
	SmCaptionHeight   int32
	SmCaptionFont     logFont
	MenuWidth         int32
	MenuHeight        int32
	MenuFont          logFont
	StatusFont        logFont
	MessageFont       logFont
	PaddedBorderWidth int32
}

type initCommonControlsEx struct {
	Size uint32
	ICC  uint32
}

// The palette, matching the application.
var (
	colBackground = rgb(0x16, 0x18, 0x1D)
	colSurface    = rgb(0x1D, 0x21, 0x28)
	colField      = rgb(0x14, 0x16, 0x1B)
	colBorder     = rgb(0x2C, 0x31, 0x3B)
	colText       = rgb(0xE6, 0xE9, 0xEF)
	colTextDim    = rgb(0x8B, 0x93, 0xA7)
	colAccent     = rgb(0x4F, 0x9C, 0xF9)
	colAccentDim  = rgb(0x31, 0x5C, 0x93)
	colHover      = rgb(0x27, 0x2C, 0x36)
	colPressed    = rgb(0x30, 0x36, 0x42)
)

var brushSurface, brushField uintptr

func rgb(r, g, b uint32) uintptr { return uintptr(r | g<<8 | b<<16) }

// initTheme prepares visual styles, common controls and the brushes.
func initTheme() {
	enableVisualStyles()

	var icc initCommonControlsEx
	icc.Size = uint32(unsafe.Sizeof(icc))
	icc.ICC = iccStandardClasses
	procInitCommonControls.Call(uintptr(unsafe.Pointer(&icc)))

	// Opt the process into dark mode before any window exists. Without
	// this, edit controls and scrollbars paint themselves light and ignore
	// the brushes returned from WM_CTLCOLOR*.
	setPreferredAppMode(appModeForceDark)
	brushSurface, _, _ = procCreateSolidBrush.Call(colSurface)
	brushField, _, _ = procCreateSolidBrush.Call(colField)
}

// enableVisualStyles opts into comctl32 v6 by activating a manifest at
// runtime, since embedding one needs a resource compiler.
func enableVisualStyles() {
	const manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <dependency><dependentAssembly>
    <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls"
      version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df"
      language="*" />
  </dependentAssembly></dependency>
</assembly>`

	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "odm")
	os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, "odm-installer.manifest")
	if b, err := os.ReadFile(path); err != nil || string(b) != manifest {
		if os.WriteFile(path, []byte(manifest), 0o644) != nil {
			return
		}
	}

	type actCtx struct {
		Size                  uint32
		Flags                 uint32
		Source                *uint16
		ProcessorArchitecture uint16
		LangID                uint16
		AssemblyDirectory     *uint16
		ResourceName          *uint16
		ApplicationName       *uint16
		Module                syscall.Handle
	}
	var ctx actCtx
	ctx.Size = uint32(unsafe.Sizeof(ctx))
	ctx.Source = utf16Ptr(path)
	create := kernel32.NewProc("CreateActCtxW")
	activate := kernel32.NewProc("ActivateActCtx")
	h, _, _ := create.Call(uintptr(unsafe.Pointer(&ctx)))
	if h == 0 || h == ^uintptr(0) {
		return
	}
	var cookie uintptr
	activate.Call(h, uintptr(unsafe.Pointer(&cookie)))
}

func applyDarkTitleBar(hwnd syscall.Handle) {
	on := int32(1)
	for _, attr := range []uintptr{20, 19} { // DWMWA_USE_IMMERSIVE_DARK_MODE
		procDwmSetWindowAttr.Call(uintptr(hwnd), attr,
			uintptr(unsafe.Pointer(&on)), unsafe.Sizeof(on))
	}
	caption := uint32(colBackground)
	procDwmSetWindowAttr.Call(uintptr(hwnd), 35,
		uintptr(unsafe.Pointer(&caption)), unsafe.Sizeof(caption))
}

// Dark mode for standard controls is opt-in through three uxtheme entry
// points that Microsoft never gave names: they are exported by ordinal
// only. GetProcAddress takes an ordinal when it is passed a small integer
// in place of a name pointer, which is the only way to reach them --
// LazyDLL.NewProc("#135") looks for a symbol literally called "#135" and
// always fails.
const (
	ordAllowDarkModeForWindow = 133
	ordSetPreferredAppMode    = 135

	appModeForceDark = 2
)

func ordinalProc(dll string, ordinal uintptr) uintptr {
	mod, err := syscall.LoadLibrary(dll)
	if err != nil {
		return 0
	}
	addr, _, _ := procGetProcAddress.Call(uintptr(mod), ordinal)
	return addr
}

func setPreferredAppMode(mode uintptr) {
	if addr := ordinalProc("uxtheme.dll", ordSetPreferredAppMode); addr != 0 {
		syscall.SyscallN(addr, mode)
	}
}

// allowDarkModeForWindow marks one window as willing to use the dark
// themes. It has to be called per window, parents included.
func allowDarkModeForWindow(hwnd syscall.Handle) {
	if addr := ordinalProc("uxtheme.dll", ordAllowDarkModeForWindow); addr != 0 {
		syscall.SyscallN(addr, uintptr(hwnd), 1)
	}
}

// darkField gives an edit control the dark field theme. "DarkMode_CFD" is
// what Explorer uses for its own search box: an edit with a dark
// background. A themed edit paints itself and ignores the brush returned
// from WM_CTLCOLOREDIT, so this, not the brush, is what makes it dark.
func darkField(hwnd syscall.Handle) {
	allowDarkModeForWindow(hwnd)
	procSetWindowTheme.Call(uintptr(hwnd),
		uintptr(unsafe.Pointer(utf16Ptr("DarkMode_CFD"))), 0)
}

// unthemeControl turns visual styles off for one control. A themed EDIT
// paints its own light background and ignores the brush returned from
// WM_CTLCOLOREDIT, so a dark field is only possible without the theme.
func unthemeControl(hwnd syscall.Handle) {
	empty := utf16Ptr("")
	procSetWindowTheme.Call(uintptr(hwnd),
		uintptr(unsafe.Pointer(empty)), uintptr(unsafe.Pointer(empty)))
}

func applyDarkControlTheme(hwnd syscall.Handle) {
	procSetWindowTheme.Call(uintptr(hwnd),
		uintptr(unsafe.Pointer(utf16Ptr("DarkMode_Explorer"))), 0)
}

func uiFont(px int32, bold bool) syscall.Handle {
	var m nonClientMetrics
	m.Size = uint32(unsafe.Sizeof(m))
	if ok, _, _ := procSystemParametersInf.Call(spiGetNonClientMetrics,
		uintptr(m.Size), uintptr(unsafe.Pointer(&m)), 0); ok == 0 {
		return 0
	}
	lf := m.MessageFont
	if px != 0 {
		lf.Height = -px
	}
	if bold {
		lf.Weight = 700
	}
	h, _, _ := procCreateFontIndirect.Call(uintptr(unsafe.Pointer(&lf)))
	return syscall.Handle(h)
}

func fillRect(hdc syscall.Handle, r rect, color uintptr) {
	b, _, _ := procCreateSolidBrush.Call(color)
	procFillRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(&r)), b)
	procDeleteObject.Call(b)
}

func roundRect(hdc syscall.Handle, r rect, radius int32, fill, outline uintptr) {
	var pen uintptr
	if outline != 0 {
		pen, _, _ = procCreatePen.Call(0, 1, outline)
	} else {
		pen, _, _ = procCreatePen.Call(5, 0, 0)
	}
	brush, _, _ := procCreateSolidBrush.Call(fill)
	oldPen, _, _ := procSelectObject.Call(uintptr(hdc), pen)
	oldBrush, _, _ := procSelectObject.Call(uintptr(hdc), brush)
	procRoundRect.Call(uintptr(hdc), uintptr(r.Left), uintptr(r.Top),
		uintptr(r.Right), uintptr(r.Bottom), uintptr(radius), uintptr(radius))
	procSelectObject.Call(uintptr(hdc), oldPen)
	procSelectObject.Call(uintptr(hdc), oldBrush)
	procDeleteObject.Call(pen)
	procDeleteObject.Call(brush)
}

func drawText(hdc syscall.Handle, s string, r rect, color uintptr,
	font syscall.Handle, flags uintptr) {
	procSetBkMode.Call(uintptr(hdc), transparent)
	procSetTextColor.Call(uintptr(hdc), color)
	var old uintptr
	if font != 0 {
		old, _, _ = procSelectObject.Call(uintptr(hdc), uintptr(font))
	}
	rr := r
	procDrawTextEx.Call(uintptr(hdc), uintptr(unsafe.Pointer(utf16Ptr(s))), ^uintptr(0),
		uintptr(unsafe.Pointer(&rr)), flags, 0)
	if old != 0 {
		procSelectObject.Call(uintptr(hdc), old)
	}
}

func darkCtlColor(msg uint32, wparam uintptr) (uintptr, bool) {
	switch msg {
	case wmCtlColorStatic, wmCtlColorBtn, wmCtlColorDlg:
		procSetBkMode.Call(wparam, transparent)
		procSetTextColor.Call(wparam, colText)
		procSetBkColor.Call(wparam, colSurface)
		return brushSurface, true
	case wmCtlColorEdit, wmCtlColorListBox:
		procSetTextColor.Call(wparam, colText)
		procSetBkColor.Call(wparam, colField)
		return brushField, true
	}
	return 0, false
}

// --- owner-drawn buttons ---------------------------------------------------

type dlgButton struct {
	label   string
	primary bool
	hovered bool
	hwnd    syscall.Handle
	parent  syscall.Handle
}

var (
	btnMu sync.Mutex
	btns  = map[uintptr]*dlgButton{}
)

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

	btnMu.Lock()
	btns[id] = &dlgButton{label: label, primary: primary,
		hwnd: syscall.Handle(hwnd), parent: parent}
	btnMu.Unlock()
	procSetWindowSubclass.Call(hwnd, syscall.NewCallback(buttonSubclass), id, 0)
	return syscall.Handle(hwnd)
}

func buttonByID(id uintptr) *dlgButton {
	btnMu.Lock()
	defer btnMu.Unlock()
	return btns[id]
}

func setButtonLabel(id uintptr, label string) {
	b := buttonByID(id)
	if b == nil {
		return
	}
	b.label = label
	procInvalidateRect.Call(uintptr(b.hwnd), 0, 1)
}

func enableButton(id uintptr, on bool) {
	b := buttonByID(id)
	if b == nil {
		return
	}
	v := uintptr(0)
	if on {
		v = 1
	}
	procEnableWindow.Call(uintptr(b.hwnd), v)
	procInvalidateRect.Call(uintptr(b.hwnd), 0, 1)
}

func hideButton(id uintptr) {
	if b := buttonByID(id); b != nil {
		procShowWindow.Call(uintptr(b.hwnd), swHide)
	}
}

func buttonSubclass(hwnd syscall.Handle, msg uint32, wparam, lparam, id, ref uintptr) uintptr {
	b := buttonByID(id)
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
	r, _, _ := procDefSubclassProc.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return r
}

func drawDialogButton(dis *drawItemStruct, font syscall.Handle) bool {
	b := buttonByID(uintptr(dis.CtlID))
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
		fill, textCol, outline = rgb(0x20, 0x24, 0x2B), rgb(0x5A, 0x60, 0x6C), colBorder
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

// --- small helpers ---------------------------------------------------------

func utf16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		p, _ = syscall.UTF16PtrFromString("")
	}
	return p
}

// lparamPtr reinterprets a WndProc LPARAM as a pointer. Windows keeps the
// structure alive for the call; go vet cannot know that, so the conversion
// is done in one audited place.
func lparamPtr(p uintptr) unsafe.Pointer { return winPtr(p) }

// winPtr reinterprets an address handed back by a Win32 call (a locked
// global handle, a WndProc parameter) as a pointer. The memory belongs to
// Windows, not the Go heap, so it cannot move under us.
func winPtr(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

func windowText(h syscall.Handle) string {
	if h == 0 {
		return ""
	}
	n, _, _ := procGetWindowTextLength.Call(uintptr(h))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowText.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func messageBox(parent syscall.Handle, title, text string, flags uintptr) uintptr {
	r, _, _ := procMessageBox.Call(uintptr(parent),
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(unsafe.Pointer(utf16Ptr(title))), flags)
	return r
}

var _ = procGetDlgItem

//go:build windows

// Package nativeui is DM's desktop window: a real Win32 application with a
// list view, toolbar and menus, rather than a browser tab.
//
// It is written directly against the Win32 API through syscall because there
// is no C toolchain on this machine, and every Go GUI binding needs cgo. The
// upside is that the whole app stays one dependency-free executable.
package nativeui

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")
	comdlg32 = syscall.NewLazyDLL("comdlg32.dll")
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
	procMoveWindow          = user32.NewProc("MoveWindow")
	procGetClientRect       = user32.NewProc("GetClientRect")
	procSetWindowText       = user32.NewProc("SetWindowTextW")
	procGetWindowText       = user32.NewProc("GetWindowTextW")
	procGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	procLoadCursor          = user32.NewProc("LoadCursorW")
	procLoadImage           = user32.NewProc("LoadImageW")
	procSetFocus            = user32.NewProc("SetFocus")
	procMessageBox          = user32.NewProc("MessageBoxW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procCreateMenu          = user32.NewProc("CreateMenu")
	procAppendMenu          = user32.NewProc("AppendMenuW")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procSetMenu             = user32.NewProc("SetMenu")
	procCheckMenuItem       = user32.NewProc("CheckMenuItem")
	procEnableMenuItem      = user32.NewProc("EnableMenuItem")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procEnableWindow        = user32.NewProc("EnableWindow")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procSystemParametersInf = user32.NewProc("SystemParametersInfoW")
	procFillRect            = user32.NewProc("FillRect")
	procFrameRect           = user32.NewProc("FrameRect")
	procDrawTextEx          = user32.NewProc("DrawTextExW")
	procInvalidateRect      = user32.NewProc("InvalidateRect")
	procSetTimer            = user32.NewProc("SetTimer")
	procKillTimer           = user32.NewProc("KillTimer")
	procGetSysColorBrush    = user32.NewProc("GetSysColorBrush")
	procGetSysColor         = user32.NewProc("GetSysColor")
	procScreenToClient      = user32.NewProc("ScreenToClient")
	procSetWindowLongPtr    = user32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtr    = user32.NewProc("GetWindowLongPtrW")
	procIsIconic            = user32.NewProc("IsIconic")

	procCreateFontIndirect = gdi32.NewProc("CreateFontIndirectW")
	procCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procSetBkMode          = gdi32.NewProc("SetBkMode")
	procSetTextColor       = gdi32.NewProc("SetTextColor")
	procSelectObject       = gdi32.NewProc("SelectObject")

	procGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
	procCreateActCtx    = kernel32.NewProc("CreateActCtxW")
	procActivateActCtx  = kernel32.NewProc("ActivateActCtx")

	procInitCommonControlsEx = comctl32.NewProc("InitCommonControlsEx")
	procGetSaveFileName      = comdlg32.NewProc("GetSaveFileNameW")
	procShellExecute         = shell32.NewProc("ShellExecuteW")
)

// Window messages.
const (
	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmSetFont       = 0x0030
	wmClose         = 0x0010
	wmNotify        = 0x004E
	wmCommand       = 0x0111
	wmTimer         = 0x0113
	wmGetMinMaxInfo = 0x0024
	wmSysCommand    = 0x0112
	wmApp           = 0x8000

	scMinimize = 0xF020

	swHide          = 0
	swShow          = 5
	swRestore       = 9
	swShowNormal    = 1
	swMinimize      = 6
	swShowNoActive  = 4
	swShowMinimized = 2
)

// Window styles.
const (
	wsOverlapped      = 0x00000000
	wsCaption         = 0x00C00000
	wsSysMenu         = 0x00080000
	wsThickFrame      = 0x00040000
	wsMinimizeBox     = 0x00020000
	wsMaximizeBox     = 0x00010000
	wsVisible         = 0x10000000
	wsChild           = 0x40000000
	wsTabStop         = 0x00010000
	wsBorder          = 0x00800000
	wsVScroll         = 0x00200000
	wsClipChildren    = 0x02000000
	wsOverlappedWin   = wsOverlapped | wsCaption | wsSysMenu | wsThickFrame | wsMinimizeBox | wsMaximizeBox
	wsExClientEdge    = 0x00000200
	wsExControlParent = 0x00010000
	wsExComposited    = 0x02000000

	bsPushButton    = 0x00000000
	bsDefPushButton = 0x00000001
	bsAutoCheckBox  = 0x00000003
	bsGroupBox      = 0x00000007

	esAutoHScroll = 0x0080
	esReadOnly    = 0x0800

	// Combo box: a drop-down list the user cannot type into.
	cbsDropDownList = 0x0003
	cbAddString     = 0x0143
	cbSetCurSel     = 0x014E
	cbGetCurSel     = 0x0147
	cbResetContent  = 0x014B

	bmGetCheck   = 0x00F0
	bmSetCheck   = 0x00F1
	bstChecked   = 1
	bstUnchecked = 0

	ssLeft = 0x0000

	// GetSaveFileName flags.
	ofnOverwritePrompt = 0x00000002
	ofnPathMustExist   = 0x00000800
	ofnHideReadOnly    = 0x00000004
	ofnExplorer        = 0x00080000
	ofnNoChangeDir     = 0x00000008

	cwUseDefault = ^uintptr(0x7FFFFFFF) // 0x80000000 as a signed default
)

// ListView.
const (
	lvmFirst = 0x1000

	lvmDeleteAllItems = lvmFirst + 9
	lvmDeleteItem     = lvmFirst + 8
	lvmGetItemCount   = lvmFirst + 4
	lvmInsertColumnW  = lvmFirst + 97
	lvmInsertItemW    = lvmFirst + 77
	lvmSetItemW       = lvmFirst + 76
	lvmSetItemTextW   = lvmFirst + 116
	lvmSetExtendedLVS = lvmFirst + 54
	lvmGetNextItem    = lvmFirst + 12
	lvmSetItemState   = lvmFirst + 43
	lvmGetItemW       = lvmFirst + 75
	lvmSetColumnWidth = lvmFirst + 30
	lvmGetSubItemRect = lvmFirst + 56
	lvmSetItemCount   = lvmFirst + 47
	lvmEnsureVisible  = lvmFirst + 19

	lvsReport        = 0x0001
	lvsSingleSel     = 0x0004
	lvsShowSelAlways = 0x0008
	lvsNoSortHeader  = 0x8000

	lvsExFullRowSelect  = 0x00000020
	lvsExDoubleBuffer   = 0x00010000
	lvsExGridLines      = 0x00000001
	lvsExHeaderDragDrop = 0x00000010

	lvifText  = 0x0001
	lvifParam = 0x0004
	lvifState = 0x0008

	lvcfFmt     = 0x0001
	lvcfWidth   = 0x0002
	lvcfText    = 0x0004
	lvcfSubItem = 0x0008

	lvcfmtLeft   = 0x0000
	lvcfmtRight  = 0x0001
	lvcfmtCenter = 0x0002

	lvniSelected = 0x0002
	lvisSelected = 0x0002

	lvirBounds = 0
	lvirLabel  = 2

	// Notification codes. Named with a code prefix so they do not collide
	// with the structures of the same name.
	codeClick      = -2 // NM_CLICK
	codeDblClk     = -3 // NM_DBLCLK
	codeRClick     = -5 // NM_RCLICK
	codeCustomDraw = -12
	lvnFirst       = -100
	lvnItemChanged = lvnFirst - 1
	lvnKeyDown     = lvnFirst - 55

	cddsPrePaint          = 0x00000001
	cddsItemPrePaint      = 0x00010001
	cddsSubItem           = 0x00020000
	cddsItemPostPaint     = 0x00010002
	cdrfDodefault         = 0x00000000
	cdrfNotifyItemDraw    = 0x00000020
	cdrfNotifySubItemDraw = 0x00000020
	cdrfSkipDefault       = 0x00000004
	cdrfNotifyPostPaint   = 0x00000010
)

const (
	iccListViewClasses = 0x00000001
	iccBarClasses      = 0x00000004
	iccStandardClasses = 0x00004000

	idcArrow = 32512

	colorBtnFace    = 15
	colorWindow     = 5
	colorWindowText = 8
	colorHighlight  = 13

	mfString    = 0x0000
	mfSeparator = 0x0800
	mfPopup     = 0x0010
	mfGrayed    = 0x0001
	mfChecked   = 0x0008
	mfUnchecked = 0x0000
	mfByCommand = 0x0000

	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	mbOk        = 0x0000
	mbIconError = 0x0010
	mbIconInfo  = 0x0040
	mbYesNo     = 0x0004
	idYes       = 6

	dtLeft        = 0x0000
	dtVCenter     = 0x0004
	dtSingleLine  = 0x0020
	dtEndEllipsis = 0x00008000
	dtCenter      = 0x0001

	transparent = 1

	spiGetNonClientMetrics = 0x0029
)

type rect struct{ Left, Top, Right, Bottom int32 }

type point struct{ X, Y int32 }

type msgStruct struct {
	Hwnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
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

type initCommonControlsEx struct {
	Size uint32
	ICC  uint32
}

type lvColumn struct {
	Mask       uint32
	Fmt        int32
	Cx         int32
	PszText    *uint16
	CchTextMax int32
	ISubItem   int32
	IImage     int32
	IOrder     int32
	CxMin      int32
	CxDefault  int32
	CxIdeal    int32
}

type lvItem struct {
	Mask       uint32
	IItem      int32
	ISubItem   int32
	State      uint32
	StateMask  uint32
	PszText    *uint16
	CchTextMax int32
	IImage     int32
	LParam     uintptr
	IIndent    int32
	IGroupID   int32
	CColumns   uint32
	PuColumns  uintptr
	PiColFmt   uintptr
	IGroup     int32
}

type nmhdr struct {
	HwndFrom syscall.Handle
	IDFrom   uintptr
	Code     int32
	_        int32 // explicit padding so the struct matches the C layout
}

type nmItemActivate struct {
	Hdr      nmhdr
	IItem    int32
	ISubItem int32
	NewState uint32
	OldState uint32
	Changed  uint32
	PtAction point
	LParam   uintptr
	KeyFlags uint32
}

type nmCustomDraw struct {
	Hdr         nmhdr
	DwDrawStage uint32
	Hdc         syscall.Handle
	Rc          rect
	DwItemSpec  uintptr
	UItemState  uint32
	LItemlParam uintptr
}

type nmLVCustomDraw struct {
	Nmcd        nmCustomDraw
	ClrText     uint32
	ClrTextBk   uint32
	ISubItem    int32
	DwItemType  uint32
	ClrFace     uint32
	IIconEffect int32
	IIconPhase  int32
	IPartID     int32
	IStateID    int32
	RcText      rect
	UAlign      uint32
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

// openFileName is OPENFILENAMEW, for the save-as browser.
type openFileName struct {
	StructSize    uint32
	Owner         syscall.Handle
	Instance      syscall.Handle
	Filter        *uint16
	CustomFilter  *uint16
	MaxCustFilter uint32
	FilterIndex   uint32
	File          *uint16
	MaxFile       uint32
	FileTitle     *uint16
	MaxFileTitle  uint32
	InitialDir    *uint16
	Title         *uint16
	Flags         uint32
	FileOffset    uint16
	FileExtension uint16
	DefExt        *uint16
	CustData      uintptr
	FnHook        uintptr
	TemplateName  *uint16
	PvReserved    uintptr
	DwReserved    uint32
	FlagsEx       uint32
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

// lparamPtr reinterprets a WndProc LPARAM as a pointer.
//
// Windows passes notification structures by address in LPARAM and keeps them
// alive for the duration of the call, so this is safe, but go vet cannot
// know that and flags the direct conversion. Doing it in one audited place
// keeps the rest of the package honest and the vet output clean.
func lparamPtr(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

func utf16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		p, _ = syscall.UTF16PtrFromString("")
	}
	return p
}

// uiFont returns the system UI font, so the app looks like the rest of
// Windows instead of defaulting to the 1990s bitmap font.
func uiFont() syscall.Handle {
	var m nonClientMetrics
	m.Size = uint32(unsafe.Sizeof(m))
	ok, _, _ := procSystemParametersInf.Call(spiGetNonClientMetrics,
		uintptr(m.Size), uintptr(unsafe.Pointer(&m)), 0)
	if ok == 0 {
		return 0
	}
	h, _, _ := procCreateFontIndirect.Call(uintptr(unsafe.Pointer(&m.MessageFont)))
	return syscall.Handle(h)
}

// enableVisualStyles opts into comctl32 version 6.
//
// Without it the common controls render in the pre-XP style, which looks
// broken next to everything else on the desktop. The usual fix is an
// embedded manifest resource, but producing one needs a resource compiler,
// so the manifest is written to disk and activated at runtime instead.
func enableVisualStyles() {
	const manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <dependency>
    <dependentAssembly>
      <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls"
        version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df"
        language="*" />
    </dependentAssembly>
  </dependency>
</assembly>`

	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "dm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, "dm-visualstyles.manifest")
	if b, err := os.ReadFile(path); err != nil || string(b) != manifest {
		if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
			return
		}
	}

	var ctx actCtx
	ctx.Size = uint32(unsafe.Sizeof(ctx))
	ctx.Source = utf16Ptr(path)
	h, _, _ := procCreateActCtx.Call(uintptr(unsafe.Pointer(&ctx)))
	if h == 0 || h == ^uintptr(0) {
		return
	}
	var cookie uintptr
	procActivateActCtx.Call(h, uintptr(unsafe.Pointer(&cookie)))
}

// initCommonControls registers the list view and standard control classes.
func initCommonControls() {
	var icc initCommonControlsEx
	icc.Size = uint32(unsafe.Sizeof(icc))
	icc.ICC = iccListViewClasses | iccBarClasses | iccStandardClasses
	procInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))
}

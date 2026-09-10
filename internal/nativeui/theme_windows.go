//go:build windows

package nativeui

import (
	"syscall"
	"unsafe"
)

// The palette. DM is dark only: a theme switch is one more thing to get
// wrong in every dialog, and the colours below are tuned as a set.
var (
	colBackground = rgb(0x16, 0x18, 0x1D) // window behind everything
	colSurface    = rgb(0x1D, 0x21, 0x28) // toolbar and status bar
	colList       = rgb(0x14, 0x16, 0x1B) // list view canvas
	colRowAlt     = rgb(0x18, 0x1B, 0x21) // alternate row banding
	colBorder     = rgb(0x2C, 0x31, 0x3B)
	colText       = rgb(0xE6, 0xE9, 0xEF)
	colTextDim    = rgb(0x8B, 0x93, 0xA7)
	colAccent     = rgb(0x4F, 0x9C, 0xF9)
	colAccentDim  = rgb(0x31, 0x5C, 0x93)
	colHover      = rgb(0x27, 0x2C, 0x36)
	colPressed    = rgb(0x30, 0x36, 0x42)
	colSelect     = rgb(0x24, 0x3B, 0x58)
	colOK         = rgb(0x3E, 0xCF, 0x8E)
	colWarn       = rgb(0xF5, 0xA5, 0x24)
	colError      = rgb(0xF4, 0x58, 0x6A)
	colTrack      = rgb(0x25, 0x2A, 0x33) // empty part of a progress bar
)

var (
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")
	uxtheme  = syscall.NewLazyDLL("uxtheme.dll")
	shell32d = syscall.NewLazyDLL("shell32.dll")

	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	procSetWindowTheme        = uxtheme.NewProc("SetWindowTheme")
	procSHGetFileInfo         = shell32d.NewProc("SHGetFileInfoW")
	procSHGetStockIconInfo    = shell32d.NewProc("SHGetStockIconInfo")
)

const (
	// DWMWA_USE_IMMERSIVE_DARK_MODE. 20 is the released value; 19 was used
	// by Windows 10 builds before 20H1, and setting both is harmless.
	dwmaUseImmersiveDarkMode       = 20
	dwmaUseImmersiveDarkModeBefore = 19
	// DWMWA_BORDER_COLOR and DWMWA_CAPTION_COLOR, Windows 11 only. They
	// fail cleanly on older builds.
	dwmaBorderColor  = 34
	dwmaCaptionColor = 35
)

// darkBrushes holds the solid brushes reused for painting, created once.
type darkBrushes struct {
	background uintptr
	surface    uintptr
	list       uintptr
	border     uintptr
}

var brushes darkBrushes

func initBrushes() {
	if brushes.background != 0 {
		return
	}
	mk := func(c uintptr) uintptr {
		h, _, _ := procCreateSolidBrush.Call(c)
		return h
	}
	brushes.background = mk(colBackground)
	brushes.surface = mk(colSurface)
	brushes.list = mk(colList)
	brushes.border = mk(colBorder)
}

// applyDarkTitleBar asks DWM for a dark caption. Without it a dark window
// wears a white title bar, which looks broken rather than themed.
func applyDarkTitleBar(hwnd syscall.Handle) {
	on := int32(1)
	for _, attr := range []uintptr{dwmaUseImmersiveDarkMode, dwmaUseImmersiveDarkModeBefore} {
		procDwmSetWindowAttribute.Call(uintptr(hwnd), attr,
			uintptr(unsafe.Pointer(&on)), unsafe.Sizeof(on))
	}
	// Windows 11 can also colour the border and caption to match.
	border := uint32(colBorder)
	procDwmSetWindowAttribute.Call(uintptr(hwnd), dwmaBorderColor,
		uintptr(unsafe.Pointer(&border)), unsafe.Sizeof(border))
	caption := uint32(colBackground)
	procDwmSetWindowAttribute.Call(uintptr(hwnd), dwmaCaptionColor,
		uintptr(unsafe.Pointer(&caption)), unsafe.Sizeof(caption))
}

// applyDarkControlTheme switches a common control to the shell's dark
// variant, which is what makes list view scrollbars and headers dark.
func applyDarkControlTheme(hwnd syscall.Handle) {
	procSetWindowTheme.Call(uintptr(hwnd),
		uintptr(unsafe.Pointer(utf16Ptr("DarkMode_Explorer"))), 0)
}

// enableDarkMode flips the process into dark mode for the parts of
// comctl32 and uxtheme that have no public API.
//
// SetPreferredAppMode is exported by ordinal only and has never been
// documented, so every call is guarded and the app is perfectly usable if
// this does nothing: the explicit colours below carry the theme, and this
// only improves scrollbars, menus and tooltips.
func enableDarkMode() {
	const (
		ordSetPreferredAppMode = 135
		ordFlushMenuThemes     = 136
		appModeForceDark       = 2
	)
	setMode := uxtheme.NewProc("#" + itoa(ordSetPreferredAppMode))
	if err := setMode.Find(); err == nil {
		setMode.Call(appModeForceDark)
	}
	flush := uxtheme.NewProc("#" + itoa(ordFlushMenuThemes))
	if err := flush.Find(); err == nil {
		flush.Call()
	}
}

// --- drawing helpers -------------------------------------------------------

// fillRect paints a solid rectangle.
func fillRect(hdc syscall.Handle, r rect, color uintptr) {
	b, _, _ := procCreateSolidBrush.Call(color)
	procFillRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(&r)), b)
	procDeleteObject.Call(b)
}

// roundRect paints a rounded rectangle, optionally outlined.
func roundRect(hdc syscall.Handle, r rect, radius int32, fill, outline uintptr) {
	var pen uintptr
	if outline != 0 {
		pen, _, _ = procCreatePen.Call(0 /*PS_SOLID*/, 1, outline)
	} else {
		pen, _, _ = procCreatePen.Call(5 /*PS_NULL*/, 0, 0)
	}
	brush, _, _ := procCreateSolidBrush.Call(fill)
	oldPen, _, _ := procSelectObject.Call(uintptr(hdc), pen)
	oldBrush, _, _ := procSelectObject.Call(uintptr(hdc), brush)

	procRoundRect.Call(uintptr(hdc),
		uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom),
		uintptr(radius), uintptr(radius))

	procSelectObject.Call(uintptr(hdc), oldPen)
	procSelectObject.Call(uintptr(hdc), oldBrush)
	procDeleteObject.Call(pen)
	procDeleteObject.Call(brush)
}

// drawText renders a string in the given colour and font.
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

// iconFont returns a handle to the Windows icon font, which ships with the
// OS and means the toolbar needs no image assets at all.
//
// Windows 11 has Segoe Fluent Icons; Windows 10 has Segoe MDL2 Assets, and
// the glyph code points used here exist in both.
func iconFont(size int32) syscall.Handle {
	for _, face := range []string{"Segoe Fluent Icons", "Segoe MDL2 Assets"} {
		var lf logFont
		lf.Height = -size
		lf.Weight = 400
		lf.CharSet = 1 // DEFAULT_CHARSET
		lf.Quality = 5 // CLEARTYPE_QUALITY
		name := syscall.StringToUTF16(face)
		copy(lf.FaceName[:], name)
		h, _, _ := procCreateFontIndirect.Call(uintptr(unsafe.Pointer(&lf)))
		if h != 0 && fontHasFace(syscall.Handle(h), face) {
			return syscall.Handle(h)
		}
		if h != 0 {
			procDeleteObject.Call(h)
		}
	}
	return 0
}

// fontHasFace checks that the font actually resolved to the face asked
// for; CreateFontIndirect happily substitutes something else otherwise,
// and the toolbar would show boxes instead of icons.
func fontHasFace(font syscall.Handle, want string) bool {
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return false
	}
	defer procReleaseDC.Call(0, hdc)

	old, _, _ := procSelectObject.Call(hdc, uintptr(font))
	buf := make([]uint16, 64)
	procGetTextFace.Call(hdc, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	procSelectObject.Call(hdc, old)
	return syscall.UTF16ToString(buf) == want
}

// uiFontOfSize builds the system UI font at a specific point-ish size, for
// the secondary text in the status bar.
func uiFontOfSize(px int32, bold bool) syscall.Handle {
	var m nonClientMetrics
	m.Size = uint32(unsafe.Sizeof(m))
	ok, _, _ := procSystemParametersInf.Call(spiGetNonClientMetrics,
		uintptr(m.Size), uintptr(unsafe.Pointer(&m)), 0)
	if ok == 0 {
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

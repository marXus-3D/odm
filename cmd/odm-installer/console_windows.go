//go:build windows

package main

import (
	"os"
	"syscall"
)

// The installer is linked as a GUI binary so double-clicking it does not
// flash a console. That leaves -silent with nowhere to print, so it borrows
// the console of whatever started it, the same trick odmd uses.
func attachConsole() {
	const (
		attachParentProcess = ^uintptr(0)
		stdOutputHandle     = ^uintptr(10) // -11
	)
	kernel32 := syscall.NewLazyDLL("kernel32.dll")

	// A redirected stdout is already going somewhere the caller chose;
	// stealing it for CONOUT$ would throw that output away.
	if h, _, _ := kernel32.NewProc("GetStdHandle").Call(stdOutputHandle); h != 0 && h != ^uintptr(0) {
		return
	}
	if r, _, _ := kernel32.NewProc("AttachConsole").Call(attachParentProcess); r == 0 {
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
		os.Stderr = out
	}
}

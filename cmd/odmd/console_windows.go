//go:build windows

package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"syscall"
)

// attachConsole reconnects stdout and stderr when odmd is run from a terminal.
//
// The binary is linked with -H windowsgui so that double-clicking it does not
// flash a console window. The cost is that it starts with no standard
// handles at all, so anything printed from a command prompt would vanish.
// Attaching to the parent's console when there is one gives back normal CLI
// behaviour without bringing the flashing window back.
func attachConsole() bool {
	// A GUI binary still inherits handles when the parent supplied them, for
	// instance when a shell redirects our output to a pipe or a file.
	// Grabbing CONOUT$ in that case would divert the output away from where
	// the caller asked for it.
	if stdoutUsable() {
		return true
	}
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	if r, _, _ := kernel32.NewProc("AttachConsole").Call(attachParentProcess); r == 0 {
		return false
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
		os.Stderr = out
		log.SetOutput(out)
		return true
	}
	return false
}

// hasConsoleWindow reports whether a console window is actually attached.
// Inherited std handles are not the same thing: a process started by
// Start-Process gets handles but no window, so nothing it prints is ever
// seen. This is the honest test for "will the user read this".
func hasConsoleWindow() bool {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	h, _, _ := kernel32.NewProc("GetConsoleWindow").Call()
	return h != 0
}

// logToFile sends the log somewhere readable when there is no console, which
// is the normal case for a background daemon.
func logToFile(stateDir string) {
	path := filepath.Join(stateDir, "odmd.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	// Keep the log from growing without bound across many restarts.
	if st, err := f.Stat(); err == nil && st.Size() > 2<<20 {
		f.Close()
		os.Rename(path, path+".old")
		if f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
			return
		}
	}
	// Keep writing to stdout too when there is somewhere for it to go, so
	// running from a terminal still shows the log live.
	if stdoutUsable() && hasConsoleWindow() {
		log.SetOutput(io.MultiWriter(os.Stdout, f))
	} else {
		log.SetOutput(f)
	}
}

// stdoutUsable reports whether we already have a standard output handle.
func stdoutUsable() bool {
	h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	return err == nil && h != 0 && h != syscall.InvalidHandle
}

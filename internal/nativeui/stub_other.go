//go:build !windows

// Package nativeui is DM's desktop window. Only Windows has a native
// implementation; elsewhere the daemon falls back to the web UI, which is
// what the caller does when Run returns an error.
package nativeui

import (
	"errors"

	"github.com/marXus-3D/dm/internal/client"
)

// ErrUnsupported means there is no native window on this platform.
var ErrUnsupported = errors.New("the native window is only implemented on Windows")

func Run(c *client.Client, onQuit func()) error { return ErrUnsupported }

// Show reports that there was no window to raise.
func Show() bool { return false }

// Quit is a no-op: there is no window to tear down.
func Quit() {}

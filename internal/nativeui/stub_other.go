//go:build !windows

// Package nativeui is DM's desktop window. Only Windows is implemented.
package nativeui

import (
	"errors"

	"github.com/marcus/dm/internal/client"
)

// ErrUnsupported means there is no native window on this platform.
var ErrUnsupported = errors.New("the native window is only implemented on Windows")

func Run(c *client.Client, onQuit func()) error { return ErrUnsupported }

func Show() bool { return false }

func Close() {}

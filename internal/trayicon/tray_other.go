//go:build !windows

// Package trayicon puts ODM in the notification area. Only Windows is
// implemented; elsewhere the daemon simply runs without a tray icon.
package trayicon

import "errors"

// MenuItem is one entry in the tray's context menu.
type MenuItem struct {
	Label     string
	Disabled  bool
	Separator bool
	OnClick   func()
}

// Tray is a running notification-area icon.
type Tray struct{}

// ErrUnsupported means this platform has no tray implementation.
var ErrUnsupported = errors.New("tray icon is only implemented on Windows")

func Run(tooltip string, items []MenuItem) (*Tray, error) { return nil, ErrUnsupported }

func (t *Tray) Stop()                     {}
func (t *Tray) Notify(title, body string) {}

// StopActive ends the running tray, if there is one.
func StopActive() {}

//go:build !windows

// Package startup registers ODM to run when the user logs in. Only Windows
// is implemented so far.
package startup

import "errors"

// ErrUnsupported means there is no login-item support on this platform.
var ErrUnsupported = errors.New("run at login is only implemented on Windows")

func Enabled() bool { return false }

func Set(on bool) error {
	if !on {
		return nil
	}
	return ErrUnsupported
}

func SetPath(exe string, on bool) error { return Set(on) }

func Supported() bool { return false }

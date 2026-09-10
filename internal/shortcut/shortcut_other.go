//go:build !windows

// Package shortcut writes Windows .lnk files. Only Windows is implemented.
package shortcut

import "errors"

// Spec describes the shortcut to write.
type Spec struct {
	Path        string
	Target      string
	Args        string
	WorkingDir  string
	Description string
	IconPath    string
	IconIndex   int32
}

// ErrUnsupported means this platform has no .lnk shortcuts.
var ErrUnsupported = errors.New("shortcuts are only implemented on Windows")

func Create(s Spec) error { return ErrUnsupported }

func StartMenuPrograms() (string, error) { return "", ErrUnsupported }

func Desktop() (string, error) { return "", ErrUnsupported }

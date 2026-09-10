//go:build !windows

// Command dm-installer only makes sense on Windows; this stub keeps the
// package building on other platforms so cross-compilation stays honest.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "dm-installer: the setup program only runs on Windows")
	os.Exit(1)
}

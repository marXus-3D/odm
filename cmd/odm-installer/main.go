//go:build windows

// Command odm-installer is the single setup program for ODM. It carries the
// binaries and the browser extension inside itself, installs them for the
// current user without needing administrator rights, and doubles as the
// uninstaller once it has copied itself into the install folder.
package main

import (
	"flag"
	"fmt"
	"os"
)

// Version is stamped into the Add or remove programs entry and the window.
const Version = "0.1.13"

func main() {
	uninstall := flag.Bool("uninstall", false, "remove ODM instead of installing it")
	silent := flag.Bool("silent", false, "run without a window")
	dir := flag.String("dir", "", "install location (default: per-user Programs folder)")
	removeData := flag.Bool("remove-data", false, "with -uninstall, also delete settings and the download list")
	noBrowser := flag.Bool("no-browser", false, "skip the browser integration")
	noStartup := flag.Bool("no-startup", false, "do not start ODM when signing in")
	noDesktop := flag.Bool("no-desktop", false, "do not create a desktop shortcut")
	flag.Parse()

	if *silent {
		attachConsole()
		os.Exit(runSilent(*uninstall, *dir, *removeData, *noBrowser, *noStartup, *noDesktop))
	}
	if err := RunWizard(*uninstall); err != nil {
		fmt.Fprintln(os.Stderr, "odm-installer:", err)
		os.Exit(1)
	}
}

func runSilent(uninstall bool, dir string, removeData, noBrowser, noStartup, noDesktop bool) int {
	say := Reporter(func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	})

	var err error
	if uninstall {
		if dir == "" {
			dir = FindInstallDir()
		}
		err = Uninstall(dir, removeData, say)
	} else {
		if dir == "" {
			dir = DefaultDir()
		}
		err = Install(Options{
			Dir:          dir,
			RunAtLogin:   !noStartup,
			DesktopIcon:  !noDesktop,
			SetUpBrowser: !noBrowser,
		}, say)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "odm-installer:", err)
		return 1
	}
	return 0
}

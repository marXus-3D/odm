package main

import (
	"log"
	"os/exec"
	"runtime"

	"github.com/marcus/dm/internal/manager"
	"github.com/marcus/dm/internal/store"
	"github.com/marcus/dm/internal/trayicon"
)

// runTray shows the notification-area icon and blocks until the tray is
// stopped. It returns false when there is no tray on this platform, in which
// case the caller should just wait for the context instead.
//
// Windows requires the thread that created a window to be the one pumping its
// messages, so this locks the goroutine to its OS thread for the duration.
func runTray(uiURL string, mgr *manager.Manager, st *store.Store, shutdown func()) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	items := []trayicon.MenuItem{
		// The first enabled entry is also what a left click runs, so the
		// most useful action goes first.
		{Label: "Open DM", OnClick: func() { openURL(uiURL) }},
		{Label: "Downloads folder", OnClick: func() {
			openPath(st.Config().Dir)
		}},
		{Separator: true},
		{Label: "Pause all downloads", OnClick: func() {
			mgr.PauseAll()
		}},
		{Separator: true},
		{Label: "Quit DM", OnClick: shutdown},
	}

	log.Printf("showing the notification-area icon")
	if _, err := trayicon.Run("DM download manager", items); err != nil {
		log.Printf("tray icon unavailable: %v", err)
		return false
	}
	log.Printf("tray icon closed")
	return true
}

// openURL hands a link to the default browser.
func openURL(url string) {
	if runtime.GOOS != "windows" {
		_ = exec.Command("xdg-open", url).Start()
		return
	}
	// rundll32 rather than "cmd /c start", so nothing goes through a shell
	// and no console window appears.
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		log.Printf("could not open %s: %v", url, err)
	}
}

// openPath opens a folder in the file manager.
func openPath(path string) {
	if path == "" {
		return
	}
	if runtime.GOOS != "windows" {
		_ = exec.Command("xdg-open", path).Start()
		return
	}
	cmd := exec.Command("explorer.exe", path)
	hideWindow(cmd)
	// Explorer returns non-zero even on success, so its status is ignored.
	_ = cmd.Start()
}

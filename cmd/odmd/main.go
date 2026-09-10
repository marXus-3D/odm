// Command odmd is the ODM download daemon: it owns the queue, serves the web
// UI on loopback, and is what the browser extension ultimately talks to.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/marXus-3D/odm/internal/api"
	"github.com/marXus-3D/odm/internal/client"
	"github.com/marXus-3D/odm/internal/engine"
	"github.com/marXus-3D/odm/internal/manager"
	"github.com/marXus-3D/odm/internal/nativeui"
	"github.com/marXus-3D/odm/internal/store"
	"github.com/marXus-3D/odm/internal/trayicon"
)

func main() {
	var (
		port       = flag.Int("port", 0, "listen port (default: from config, else 9111)")
		stateDir   = flag.String("state", store.StateDir(), "state directory")
		printURL   = flag.Bool("print-url", false, "print the UI url and token, then exit")
		background = flag.Bool("background", false,
			"started automatically; do not open the web UI")
		noTray   = flag.Bool("no-tray", false, "do not show a notification-area icon")
		noWindow = flag.Bool("no-window", false, "run headless; do not open the desktop window")
		openUI   = flag.Bool("open", false, "open the web UI in a browser instead of the desktop window")
	)
	flag.Parse()

	// Linked as a GUI binary so double-clicking does not flash a console.
	// When it was in fact run from a terminal, reattach so output still
	// appears; otherwise send the log to a file.
	hasConsole := attachConsole()

	log.SetFlags(log.Ltime)
	log.SetPrefix("odmd: ")

	// Always keep a log on disk unless the user is watching a real console;
	// a daemon launched from Explorer or by the browser otherwise leaves no
	// trace of why it failed.
	_ = hasConsole
	logToFile(*stateDir)

	st, err := store.Open(*stateDir, engine.DefaultDownloadDir())
	if err != nil {
		log.Fatalf("open state: %v", err)
	}
	token, err := st.Token()
	if err != nil {
		log.Fatalf("token: %v", err)
	}

	cfg := st.Config()
	if *port > 0 {
		cfg.Port = *port
	}
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)

	if *printURL {
		fmt.Printf("http://%s/\ntoken: %s\n", addr, token)
		return
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A separate cancel so the API can request the same graceful shutdown
	// that Ctrl-C triggers.
	ctx, shutdown := context.WithCancel(sigCtx)
	defer shutdown()

	mgr := manager.New(ctx, st)
	// The "Exit ODM" completion action needs a way to stop the daemon.
	mgr.SetExitFunc(func() { shutdown() })
	mgr.StartFlusher(ctx, 2*time.Second)

	srv := api.New(mgr, st, token, addr, shutdown)
	ln, httpSrv, err := srv.Listen()
	if err != nil {
		log.Fatalf("listen on %s: %v (is another odmd already running?)", addr, err)
	}

	// The port is written where the CLI and the native messaging host can
	// find it without being told.
	portFile := st.Dir() + string(os.PathSeparator) + "port"
	if err := os.WriteFile(portFile, []byte(fmt.Sprint(cfg.Port)), 0o600); err != nil {
		log.Printf("warning: could not record port: %v", err)
	}
	defer os.Remove(portFile)

	uiURL := fmt.Sprintf("http://%s/", addr)
	log.Printf("listening on %s", uiURL)
	log.Printf("downloads go to %s", cfg.Dir)

	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("serve: %v", err)
		}
	}()

	// Launched by hand rather than by the browser, so show the user
	// something: otherwise double-clicking the binary looks like nothing
	// happened at all.
	showWindow := !*background && !*noWindow && !*openUI
	if *openUI || (!*background && *noWindow) {
		openURL(uiURL)
	}

	// Windows delivers messages to the thread that created a window, so the
	// tray and the main window each get their own locked thread and pump
	// their own loop.
	go func() {
		<-ctx.Done()
		trayicon.StopActive()
		nativeui.Quit()
	}()

	if !*noTray {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			runTray(uiURL, mgr, st, shutdown)
		}()
	}

	if showWindow {
		runtime.LockOSThread()
		err := nativeui.Run(client.NewLocal(uiURL, token), shutdown)
		runtime.UnlockOSThread()
		if err != nil {
			// No desktop window on this platform, or it failed to start:
			// fall back to the browser rather than leaving nothing.
			log.Printf("desktop window unavailable: %v", err)
			openURL(uiURL)
			<-ctx.Done()
		}
	} else {
		<-ctx.Done()
	}
	shutdown()
	log.Printf("shutting down, saving resume state...")

	// Pause first so every worker writes its resume sidecar.
	mgr.PauseAll()
	time.Sleep(300 * time.Millisecond) // let in-flight sidecar writes land

	// Persist the download list while the listener is still bound. The bound
	// port is what stops a second daemon from starting, so flushing after we
	// release it opens a window where a new daemon loads the old list and our
	// write then clobbers whatever it does next.
	if err := st.Flush(); err != nil {
		log.Printf("final flush: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)

	// A pause that landed during Shutdown still needs recording; the port is
	// gone by now, but so is any writer that could race us.
	if err := st.Flush(); err != nil {
		log.Printf("post-shutdown flush: %v", err)
	}
	log.Printf("bye")
}

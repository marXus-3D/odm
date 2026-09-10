// Command dmd is the DM download daemon: it owns the queue, serves the web
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
	"syscall"
	"time"

	"github.com/marcus/dm/internal/api"
	"github.com/marcus/dm/internal/engine"
	"github.com/marcus/dm/internal/manager"
	"github.com/marcus/dm/internal/store"
)

func main() {
	var (
		port     = flag.Int("port", 0, "listen port (default: from config, else 9111)")
		stateDir = flag.String("state", store.StateDir(), "state directory")
		printURL = flag.Bool("print-url", false, "print the UI url and token, then exit")
	)
	flag.Parse()

	log.SetFlags(log.Ltime)
	log.SetPrefix("dmd: ")

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
	mgr.StartFlusher(ctx, 2*time.Second)

	srv := api.New(mgr, st, token, addr, shutdown)
	ln, httpSrv, err := srv.Listen()
	if err != nil {
		log.Fatalf("listen on %s: %v (is another dmd already running?)", addr, err)
	}

	// The port is written where the CLI and the native messaging host can
	// find it without being told.
	portFile := st.Dir() + string(os.PathSeparator) + "port"
	if err := os.WriteFile(portFile, []byte(fmt.Sprint(cfg.Port)), 0o600); err != nil {
		log.Printf("warning: could not record port: %v", err)
	}
	defer os.Remove(portFile)

	log.Printf("listening on http://%s/", addr)
	log.Printf("downloads go to %s", cfg.Dir)

	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("serve: %v", err)
		}
	}()

	<-ctx.Done()
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

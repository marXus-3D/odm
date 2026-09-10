// Package api exposes the daemon over loopback HTTP for the web UI, the CLI
// and the browser extension's native messaging host.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marcus/dm/internal/hls"
	"github.com/marcus/dm/internal/manager"
	"github.com/marcus/dm/internal/startup"
	"github.com/marcus/dm/internal/store"
)

// Server wires the manager to HTTP handlers.
type Server struct {
	mgr   *manager.Manager
	st    *store.Store
	token string
	addr  string

	// shutdown asks the daemon to stop. It is how "dm daemon stop" reaches
	// the same graceful path as Ctrl-C, which matters on Windows where a
	// console process with no window cannot be signalled from outside.
	shutdown func()
}

func New(mgr *manager.Manager, st *store.Store, token, addr string, shutdown func()) *Server {
	return &Server{mgr: mgr, st: st, token: token, addr: addr, shutdown: shutdown}
}

// Handler builds the routing table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/state", s.guard(s.handleState))
	mux.HandleFunc("POST /api/downloads", s.guard(s.handleAdd))
	mux.HandleFunc("POST /api/downloads/{id}/pause", s.guard(s.handlePause))
	mux.HandleFunc("POST /api/downloads/{id}/resume", s.guard(s.handleResume))
	mux.HandleFunc("POST /api/downloads/{id}/confirm", s.guard(s.handleConfirm))
	mux.HandleFunc("DELETE /api/downloads/{id}", s.guard(s.handleRemove))
	mux.HandleFunc("GET /api/downloads/{id}/progress", s.guard(s.handleProgress))
	mux.HandleFunc("POST /api/downloads/{id}/reveal", s.guard(s.handleReveal))
	mux.HandleFunc("POST /api/downloads/{id}/open", s.guard(s.handleOpen))
	mux.HandleFunc("PUT /api/config", s.guard(s.handleSetConfig))
	mux.HandleFunc("POST /api/all/pause", s.guard(s.handlePauseAll))
	mux.HandleFunc("POST /api/all/resume", s.guard(s.handleResumeAll))
	mux.HandleFunc("POST /api/all/stop", s.guard(s.handleStopAll))
	mux.HandleFunc("PUT /api/flags", s.guard(s.handleSetFlags))
	mux.HandleFunc("GET /api/events", s.guard(s.handleEvents))
	mux.HandleFunc("POST /api/shutdown", s.guard(s.handleShutdown))
	mux.HandleFunc("GET /api/ping", s.guard(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))

	mux.HandleFunc("GET /", s.handleUI)
	return logRequests(mux)
}

// Listen starts the HTTP server bound to loopback only.
func (s *Server) Listen() (net.Listener, *http.Server, error) {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return nil, nil, err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return ln, srv, nil
}

// guard is the security boundary for the whole API.
//
// The daemon listens on loopback, which any page in the user's browser can
// also reach. Two checks keep a hostile page out:
//
//  1. A shared secret. It lives in a 0600 file in the state directory, so
//     only local processes running as this user can read it. A web page
//     cannot.
//  2. An Origin check. Browsers attach Origin to cross-site requests; we
//     reject every origin except our own UI. Requiring the token in a custom
//     header also forces a CORS preflight, which we never approve, so the
//     browser blocks such requests before they are even sent.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin) {
			http.Error(w, "cross-origin requests are not accepted", http.StatusForbidden)
			return
		}
		// EventSource cannot set headers, so SSE may pass the token as a query
		// parameter instead. Everything else uses the header.
		tok := r.Header.Get("X-DM-Token")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// originAllowed accepts only our own loopback UI. Extensions talk to the
// daemon through the native messaging host, never straight from a page, so
// no extension origin needs to be allowed here.
func (s *Server) originAllowed(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

type addRequest struct {
	URL      string            `json:"url"`
	Filename string            `json:"filename,omitempty"`
	Dir      string            `json:"dir,omitempty"`
	MaxConns int               `json:"maxConns,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Referer  string            `json:"referer,omitempty"`
	Cookie   string            `json:"cookie,omitempty"`
	UA       string            `json:"userAgent,omitempty"`

	// Kind lets a caller that already sniffed the content type say so:
	// "hls" for a playlist, "file" for anything else. Empty means detect.
	Kind string `json:"kind,omitempty"`

	Category    string `json:"category,omitempty"`
	Description string `json:"description,omitempty"`
	NoPrompt    bool   `json:"noPrompt,omitempty"`
}

func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		http.Error(w, "only http and https urls are supported", http.StatusBadRequest)
		return
	}

	headers := map[string]string{}
	for k, v := range req.Headers {
		headers[k] = v
	}
	// Convenience fields, so callers do not have to build the header map.
	if req.Referer != "" {
		headers["Referer"] = req.Referer
	}
	if req.Cookie != "" {
		headers["Cookie"] = req.Cookie
	}
	if req.UA != "" {
		headers["User-Agent"] = req.UA
	}

	rec, err := s.mgr.Add(manager.AddRequest{
		URL:         req.URL,
		Filename:    req.Filename,
		Dir:         req.Dir,
		MaxConns:    req.MaxConns,
		Headers:     headers,
		Kind:        req.Kind,
		Category:    req.Category,
		Description: req.Description,
		NoPrompt:    req.NoPrompt,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"downloads": s.mgr.List(),
		"config":    s.st.Config(),
		// Whether video downloads end up as .mp4 or .ts depends entirely on
		// this, so the UI should be able to say so rather than leave the
		// user guessing.
		"ffmpeg": hls.FFmpegPath(),
		// Reported from the system rather than the config file, so a login
		// item removed behind our back is shown accurately.
		"startWithWindows":          startup.Enabled(),
		"startWithWindowsSupported": startup.Supported(),
		"extensionSeen":             s.st.ExtensionSeen(),
	})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.act(w, r, s.mgr.Pause)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.act(w, r, s.mgr.Resume)
}

func (s *Server) act(w http.ResponseWriter, r *http.Request, fn func(string) error) {
	if err := fn(r.PathValue("id")); err != nil {
		status := http.StatusInternalServerError
		if err == manager.ErrNotFound {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	del := r.URL.Query().Get("deleteFile") == "true"
	if err := s.mgr.Remove(r.PathValue("id"), del); err != nil {
		status := http.StatusInternalServerError
		if err == manager.ErrNotFound {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleConfirm answers the Download File Info dialog.
func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	var c struct {
		Dir         string `json:"dir,omitempty"`
		Filename    string `json:"filename,omitempty"`
		Category    string `json:"category,omitempty"`
		Description string `json:"description,omitempty"`
		Start       bool   `json:"start"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&c); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	err := s.mgr.Confirm(r.PathValue("id"), manager.Confirmation{
		Dir: c.Dir, Filename: c.Filename, Category: c.Category,
		Description: c.Description, Start: c.Start,
	})
	if err != nil {
		status := http.StatusInternalServerError
		if err == manager.ErrNotFound {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	st, ok := s.mgr.Progress(r.PathValue("id"))
	if !ok {
		http.Error(w, "download is not running", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handlePauseAll(w http.ResponseWriter, r *http.Request) {
	s.mgr.PauseAll()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleResumeAll(w http.ResponseWriter, r *http.Request) {
	n := s.mgr.ResumeAll()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "affected": n})
}

func (s *Server) handleStopAll(w http.ResponseWriter, r *http.Request) {
	n := s.mgr.StopAll()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "affected": n})
}

// handleSetFlags carries the boolean settings, which cannot ride along with
// the merge-non-empty rules the rest of the config uses.
func (s *Server) handleSetFlags(w http.ResponseWriter, r *http.Request) {
	var f struct {
		StartWithWindows         *bool `json:"startWithWindows,omitempty"`
		ShowStartDialog          *bool `json:"showStartDialog,omitempty"`
		ShowCompleteDialog       *bool `json:"showCompleteDialog,omitempty"`
		ExtensionPromptDismissed *bool `json:"extensionPromptDismissed,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&f); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.st.SetFlags(store.Flags{
		StartWithWindows:         f.StartWithWindows,
		ShowStartDialog:          f.ShowStartDialog,
		ShowCompleteDialog:       f.ShowCompleteDialog,
		ExtensionPromptDismissed: f.ExtensionPromptDismissed,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Registering at login touches the system, so it follows the setting
	// rather than being a separate switch the user has to find.
	if f.StartWithWindows != nil && startup.Supported() {
		if err := startup.Set(*f.StartWithWindows); err != nil {
			log.Printf("run at login: %v", err)
		}
	}
	writeJSON(w, http.StatusOK, s.st.Config())
}

func (s *Server) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var c store.Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&c); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	// The port only takes effect on restart; changing it here would leave the
	// running listener and the stored value disagreeing.
	c.Port = 0
	if err := s.st.SetConfig(c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Apply the throughput ceiling to downloads already in flight rather than
	// waiting for the next one to start.
	s.mgr.SetLimit(s.st.Config().LimitKBps)
	writeJSON(w, http.StatusOK, s.st.Config())
}

// handleEvents streams changes as Server-Sent Events. SSE rather than
// WebSocket: the traffic is one-way, and EventSource reconnects on its own
// without a dependency.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, unsub := s.mgr.Subscribe()
	defer unsub()

	// A heartbeat keeps intermediaries and idle-connection reapers from
	// dropping a quiet stream.
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// handleShutdown answers before stopping, so the caller sees a clean reply
// rather than a dropped connection.
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if s.shutdown != nil {
		go s.shutdown()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("dm: write response: %v", err)
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/events") {
			next.ServeHTTP(w, r) // do not log long-lived streams
			return
		}
		next.ServeHTTP(w, r)
	})
}

package api

import (
	"embed"
	"html/template"
	"net/http"
	"path"
	"strings"
)

// uiFS holds the web UI. Embedding keeps the daemon a single file with no
// install-time asset paths to get wrong.
//
//go:embed all:assets
var uiFS embed.FS

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	clean := path.Clean(r.URL.Path)
	if clean == "/" || clean == "." {
		s.serveIndex(w, r)
		return
	}
	// Everything else is a static asset. path.Clean plus the leading-slash
	// check keeps "../" out of the embedded filesystem lookup.
	name := "assets" + clean
	if strings.Contains(clean, "..") {
		http.NotFound(w, r)
		return
	}
	b, err := uiFS.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch path.Ext(name) {
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(b)
}

// serveIndex injects the API token into the page. The UI is same-origin with
// the API, so handing it the secret here avoids asking the user to paste it.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	raw, err := uiFS.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, "ui not built", http.StatusInternalServerError)
		return
	}
	tpl, err := template.New("index").Parse(string(raw))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The page loads nothing from the network: everything it needs is inline
	// or same-origin, so the policy can be strict.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'self' 'unsafe-inline'; "+
			"style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
	if err := tpl.Execute(w, map[string]any{"Token": s.token}); err != nil {
		return
	}
}

func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request) {
	s.withPath(w, r, revealInFileManager)
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	s.withPath(w, r, openWithDefaultApp)
}

// withPath resolves an id to the path we recorded for it. Only paths the
// daemon itself chose are ever handed to the OS: the caller supplies an id,
// never a filename.
func (s *Server) withPath(w http.ResponseWriter, r *http.Request, fn func(string) error) {
	rec, err := s.mgr.Get(r.PathValue("id"))
	if err != nil {
		http.Error(w, "download not found", http.StatusNotFound)
		return
	}
	if rec.Path == "" {
		http.Error(w, "download has no file yet", http.StatusConflict)
		return
	}
	if err := fn(rec.Path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Command dm-site serves the landing page drafts in ./site. Each draft is
// reachable at /1 .. /5, and the page itself carries a switcher to jump
// between them. It is a development tool: nothing in the product uses it.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

var draft = regexp.MustCompile(`^/([1-9])/?$`)

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "address to listen on")
	dir := flag.String("dir", "site", "folder holding pages/ and shared/")
	flag.Parse()

	if _, err := os.Stat(filepath.Join(*dir, "pages")); err != nil {
		fmt.Fprintf(os.Stderr, "dm-site: %s has no pages folder\n", *dir)
		os.Exit(1)
	}

	files := http.FileServer(http.Dir(*dir))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/1", http.StatusFound)
			return
		}
		if m := draft.FindStringSubmatch(r.URL.Path); m != nil {
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFile(w, r, filepath.Join(*dir, "pages", m[1]+".html"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})

	fmt.Printf("landing page drafts at http://%s/1\n", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		fmt.Fprintln(os.Stderr, "dm-site:", err)
		os.Exit(1)
	}
}

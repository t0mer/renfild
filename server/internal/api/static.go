package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/t0mer/renfild/internal/webui"
)

// SPAHandler serves the embedded single-page app: real files when they exist,
// index.html for every client-side route.
func SPAHandler() http.Handler {
	site, err := webui.FS()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusInternalServerError, "embedded web UI is unavailable")
		})
	}
	files := http.FileServer(http.FS(site))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." {
			serveIndex(w, r, site)
			return
		}
		if _, err := fs.Stat(site, name); err != nil {
			// Unknown path: hand it to the router in the browser.
			serveIndex(w, r, site)
			return
		}
		// Hashed asset filenames are safe to cache hard; index.html is not.
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, site fs.FS) {
	page, err := fs.ReadFile(site, "index.html")
	if err != nil {
		writeError(w, http.StatusNotFound, "web UI not built into this binary")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write(page)
}

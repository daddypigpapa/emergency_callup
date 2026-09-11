package httpapi

import (
	"net/http"
	"strings"

	"emergencycallup/web"
)

// registerStatic serves the embedded web/ frontend (SPEC §3.3: go:embed).
// HTML is served with no-cache (SPA shells change often); everything else
// gets a long cache lifetime since filenames are expected to carry a
// ?v={BUILD_ID} cache-buster (SPEC §13.2).
func (s *Server) registerStatic(mux *http.ServeMux) {
	fileServer := http.FileServer(http.FS(web.Files))

	handler := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".html") || strings.HasSuffix(r.URL.Path, "/") {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	}

	// http.FileServer already serves a directory request (e.g. "/a/") by
	// finding "index.html" within it and serving that content in place —
	// no path rewriting needed. (An earlier version rewrote "/a/" to
	// "/a/index.html" itself, but http.FileServer 301-redirects any URL
	// that literally *ends* in "/index.html" back to "./" for
	// canonicalization, which turned that rewrite into an infinite
	// redirect loop.)
	mux.HandleFunc("GET /f/", handler)
	mux.HandleFunc("GET /a/", handler)
	mux.HandleFunc("GET /shared/", handler)
	mux.HandleFunc("GET /vendor/", handler)
	mux.HandleFunc("GET /sw.js", handler)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/f/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
}

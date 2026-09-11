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

	mux.HandleFunc("GET /f/", redirectDir(handler, "/f/", "/f/index.html"))
	mux.HandleFunc("GET /a/", redirectDir(handler, "/a/", "/a/index.html"))
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

// redirectDir serves indexPath when the request is exactly the directory
// root, otherwise delegates to the file server (which itself 404s on a
// missing sub-path — there is no client-side router to fall back to since
// each screen is its own static HTML file, SPEC §3.3).
func redirectDir(next http.HandlerFunc, dir, indexPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == dir {
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = indexPath
			next(w, r2)
			return
		}
		next(w, r)
	}
}

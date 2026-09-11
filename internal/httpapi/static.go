package httpapi

import (
	"net/http"

	"emergencycallup/web"
)

// registerStatic serves the embedded web/ frontend (SPEC §3.3: go:embed).
//
// DECISION: SPEC §13.2 calls for a 1-year immutable cache on non-HTML
// assets, keyed off a "?v={BUILD_ID}" cache-buster in the HTML that
// references them. This build doesn't yet generate a real per-release
// BUILD_ID (the HTML hardcodes "?v=1"), so a long immutable cache would
// silently serve stale CSS/JS after every deploy — which is exactly what
// happened during development here. Until a real BUILD_ID exists, every
// response (HTML included) is "no-cache": the browser always revalidates
// with the server instead of trusting a cached copy blindly. Swap this back
// to the long-cache branch once BUILD_ID is wired up.
func (s *Server) registerStatic(mux *http.ServeMux) {
	fileServer := http.FileServer(http.FS(web.Files))

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
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

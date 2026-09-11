package httpapi

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// securityHeaders applies the fixed header set from SPEC §11.2 to every
// response. tileOrigin is the scheme+host of TILE_URL (e.g.
// "https://api.vworld.kr"), added to img-src so the map tiles can load.
// tlsEnabled controls whether HSTS is sent (only meaningful when this
// process terminates TLS itself or is known to always sit behind HTTPS).
func securityHeaders(tileOrigin string, hstsEnabled bool) func(http.Handler) http.Handler {
	csp := fmt.Sprintf(
		"default-src 'self'; script-src 'self'; style-src 'self'; "+
			"img-src 'self' data: %s; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'",
		tileOrigin)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Permissions-Policy", "geolocation=(self), camera=(), microphone=()")
			if hstsEnabled {
				h.Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// tileOriginFrom extracts "scheme://host" from a tile URL template (which
// may contain literal "{key}"/"{z}"/"{x}"/"{y}" placeholders — url.Parse
// tolerates these in the path).
func tileOriginFrom(tileURL string) string {
	u, err := url.Parse(tileURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// multipartExemptPath is the one route allowed to use multipart/form-data
// instead of JSON (SPEC §7.1: "예외: POST /a/members/import만
// multipart/form-data 허용").
const multipartExemptPath = "/api/v1/a/members/import"

// requireJSONContentType enforces SPEC §7.1: state-changing requests must
// carry Content-Type: application/json, except the documented multipart
// exception for member import, or a 415 is returned.
func requireJSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == multipartExemptPath {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			ct := r.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				w.WriteHeader(http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// gzipResponses gzips response bodies over ~1KB (SPEC §7.1: "1KB 넘는 응답은
// gzip"), when the client advertises support.
func gzipResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.Close()
		next.ServeHTTP(gw, r)
	})
}

// gzipResponseWriter buffers up to a small threshold before deciding whether
// to compress, so tiny responses (most of this API, by design — SPEC §2.1)
// aren't wastefully wrapped in a gzip stream that costs more than it saves.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz        *gzip.Writer
	buf       []byte
	status    int
	decided   bool
	threshold int
}

const gzipThreshold = 1024

func (g *gzipResponseWriter) WriteHeader(status int) {
	g.status = status
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if g.decided {
		if g.gz != nil {
			return g.gz.Write(p)
		}
		return g.ResponseWriter.Write(p)
	}
	g.buf = append(g.buf, p...)
	if len(g.buf) < gzipThreshold {
		return len(p), nil
	}
	g.flushDecision(true)
	return len(p), nil
}

func (g *gzipResponseWriter) flushDecision(overThreshold bool) {
	g.decided = true
	if g.status == 0 {
		g.status = http.StatusOK
	}
	if overThreshold {
		g.Header().Set("Content-Encoding", "gzip")
		g.Header().Del("Content-Length")
		g.ResponseWriter.WriteHeader(g.status)
		g.gz = gzip.NewWriter(g.ResponseWriter)
		_, _ = g.gz.Write(g.buf)
	} else {
		g.ResponseWriter.WriteHeader(g.status)
		_, _ = g.ResponseWriter.Write(g.buf)
	}
	g.buf = nil
}

func (g *gzipResponseWriter) Close() error {
	if !g.decided {
		g.flushDecision(false)
	}
	if g.gz != nil {
		return g.gz.Close()
	}
	return nil
}

var _ io.Writer = (*gzipResponseWriter)(nil)

// chain composes middlewares in the order given (first wraps outermost).
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

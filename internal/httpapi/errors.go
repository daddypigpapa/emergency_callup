package httpapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// apiError is the common error body shape (SPEC §7.1):
//
//	{"t":..., "err":"코드", "msg":"사람이 읽을 문장"}
type apiError struct {
	T   int64  `json:"t"`
	Err string `json:"err"`
	Msg string `json:"msg"`
	// RetryAfter, in seconds, set for "rate" and "busy" errors.
	RetryAfter int `json:"retryAfter,omitempty"`
}

// writeError writes a JSON error body with the HTTP status matching code,
// per the table in SPEC §7.1.
func writeError(w http.ResponseWriter, now time.Time, code, msg string) {
	writeErrorRetry(w, now, code, msg, 0)
}

func writeErrorRetry(w http.ResponseWriter, now time.Time, code, msg string, retryAfterSec int) {
	status := statusForCode(code)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if retryAfterSec > 0 {
		w.Header().Set("Retry-After", itoa(retryAfterSec))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{T: now.UnixMilli(), Err: code, Msg: msg, RetryAfter: retryAfterSec})
}

func statusForCode(code string) int {
	switch code {
	case "auth":
		return http.StatusUnauthorized
	case "forbidden":
		return http.StatusForbidden
	case "invalid":
		return http.StatusBadRequest
	case "conflict", "resync", "closed":
		return http.StatusConflict
	case "not_found":
		return http.StatusNotFound
	case "rate":
		return http.StatusTooManyRequests
	case "busy":
		return http.StatusServiceUnavailable
	case "upstream":
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// writeJSON writes v as a JSON response body with status 200.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

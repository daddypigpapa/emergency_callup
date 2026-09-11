package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"emergencycallup/internal/config"
	"emergencycallup/internal/store"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.OpenMemory(t.Name())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg, err := config.Load(func(k string) string {
		m := map[string]string{
			"BASE_URL":      "https://mob.example.go.kr",
			"DATA_DIR":      "/data",
			"TILE_URL":      "https://api.vworld.kr/req/wmts/1.0.0/{key}/Base/{z}/{y}/{x}.png",
			"TILE_KEY":      "testkey",
			"NAVER_APPNAME": "mob.example.go.kr",
		}
		return m[k]
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return NewServer(cfg, db)
}

func TestHealthz_NoAuthRequired(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["ok"] != true {
		t.Errorf("ok = %v, want true", body["ok"])
	}
}

// Security headers must be present on every response (SPEC §11.2).
func TestSecurityHeaders_Present(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	checks := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for k, want := range checks {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("missing CSP header")
	}
	if !bytes.Contains([]byte(csp), []byte("script-src 'self'")) {
		t.Errorf("CSP missing script-src 'self': %s", csp)
	}
	if !bytes.Contains([]byte(csp), []byte("https://api.vworld.kr")) {
		t.Errorf("CSP img-src missing tile origin: %s", csp)
	}
	perm := rec.Header().Get("Permissions-Policy")
	if !bytes.Contains([]byte(perm), []byte("geolocation=(self)")) {
		t.Errorf("Permissions-Policy missing geolocation=(self): %s", perm)
	}
}

func postJSON(t *testing.T, h http.Handler, path string, body any, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAdminLogin_WrongContentType_415(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/a/login", bytes.NewReader([]byte(`{}`)))
	// no Content-Type header set
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestAdminLogin_SuccessAndSessionRoundtrip(t *testing.T) {
	s := testServer(t)
	if _, err := s.Admins.Create(context.Background(), "admin1", "strongpass1", "관리자", "", "admin", s.Now()); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	h := s.Handler()

	rec := postJSON(t, h, "/api/v1/a/login", adminLoginRequest{ID: "admin1", PW: "strongpass1"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected sid cookie to be set")
	}
}

func TestAdminLogin_WrongPassword_401(t *testing.T) {
	s := testServer(t)
	if _, err := s.Admins.Create(context.Background(), "admin1", "strongpass1", "관리자", "", "admin", s.Now()); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	h := s.Handler()
	rec := postJSON(t, h, "/api/v1/a/login", adminLoginRequest{ID: "admin1", PW: "wrongpass"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// R11: garbage cookie value must yield 401, never 500.
func TestRequireAdminSession_InvalidCookie_401(t *testing.T) {
	s := testServer(t)
	var gotCode int
	handler := s.requireAdminSession("operator", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/a/snapshot", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "%E0%A4%A-garbage"})
	rec := httptest.NewRecorder()
	handler(rec, req)
	gotCode = rec.Code
	if gotCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", gotCode)
	}
}

func TestRequireAdminSession_NoCookie_401(t *testing.T) {
	s := testServer(t)
	handler := s.requireAdminSession("operator", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/a/snapshot", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRequireAdminSession_RoleEnforced(t *testing.T) {
	s := testServer(t)
	admin, err := s.Admins.Create(context.Background(), "op1", "strongpass1", "운영자", "", "operator", s.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	raw, err := s.Sessions.Create(context.Background(), "admin", admin.ID, s.Now())
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	handler := s.requireAdminSession("admin", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/a/members", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("operator hitting admin-only route: status = %d, want 403", rec.Code)
	}
}

// R7: 5+ consecutive failures against the same login ID triggers increasing
// backoff at the HTTP layer.
func TestAdminLogin_BackoffAfterRepeatedFailures(t *testing.T) {
	s := testServer(t)
	if _, err := s.Admins.Create(context.Background(), "admin1", "strongpass1", "관리자", "", "admin", s.Now()); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	h := s.Handler()

	for i := 0; i < 5; i++ {
		rec := postJSON(t, h, "/api/v1/a/login", adminLoginRequest{ID: "admin1", PW: "wrong"}, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}
	// 6th attempt (with correct password!) should now be throttled.
	rec := postJSON(t, h, "/api/v1/a/login", adminLoginRequest{ID: "admin1", PW: "strongpass1"}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 after repeated failures", rec.Code)
	}
}

// R14: 100 concurrent (failing, so they all hash) logins must not blow up
// /healthz latency thanks to the bcrypt semaphore.
func TestConcurrentLogins_DoNotStarveHealthz(t *testing.T) {
	s := testServer(t)
	if _, err := s.Admins.Create(context.Background(), "admin1", "strongpass1", "관리자", "", "admin", s.Now()); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	h := s.Handler()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Use distinct login IDs so the per-ID backoff doesn't mask the
			// concurrency behavior we're testing here.
			postJSON(t, h, "/api/v1/a/login", adminLoginRequest{ID: "nouser" + itoa(i), PW: "whatever1"}, nil)
		}(i)
	}

	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	elapsed := time.Since(start)
	wg.Wait()

	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("healthz took %v under load, want < 300ms", elapsed)
	}
}

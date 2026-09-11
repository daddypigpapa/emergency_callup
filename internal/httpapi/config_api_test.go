package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GET /api/v1/config must embed the effective tile key (env-var default
// when no admin override has been saved).
func TestHandleConfig_UsesEnvKeyByDefault(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	req := httptest.NewRequest("GET", "/api/v1/config", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body["tileUrl"].(string), "testkey") {
		t.Errorf("tileUrl = %v, want it to contain the env TILE_KEY", body["tileUrl"])
	}
}

func getTileKey(t *testing.T, h http.Handler, cookie string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/a/settings/tile-key", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode (status %d, body %s): %v", rec.Code, rec.Body.String(), err)
	}
	return body
}

// An admin can set a VWorld key from the web UI (PUT /a/settings/tile-key),
// and it immediately takes effect for GET /api/v1/config — which both the
// field and admin screens read from — without a server restart.
func TestTileKeySetting_AdminOverrideAppliesImmediately(t *testing.T) {
	s := testServer(t)
	admin, err := s.Admins.Create(context.Background(), "admin1", "strongpass1", "관리자", "", "admin", s.Now())
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	raw, err := s.Sessions.Create(context.Background(), "admin", admin.ID, s.Now())
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	h := s.Handler()

	before := getTileKey(t, h, raw)
	if before["key"] != "testkey" || before["source"] != "env" {
		t.Fatalf("before override: %+v", before)
	}

	putReq := httptest.NewRequest("PUT", "/api/v1/a/settings/tile-key", strings.NewReader(`{"key":"REAL-KEY-1234"}`))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, putReq)
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d body=%s", rec.Code, rec.Body.String())
	}

	cfgReq := httptest.NewRequest("GET", "/api/v1/config", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, cfgReq)
	var cfg map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if !strings.Contains(cfg["tileUrl"].(string), "REAL-KEY-1234") {
		t.Errorf("tileUrl after override = %v, want it to contain REAL-KEY-1234", cfg["tileUrl"])
	}

	after := getTileKey(t, h, raw)
	if after["key"] != "REAL-KEY-1234" || after["source"] != "admin" {
		t.Fatalf("after override: %+v", after)
	}
}

func TestTileKeySetting_RequiresAdminRole(t *testing.T) {
	s := testServer(t)
	op, err := s.Admins.Create(context.Background(), "op1", "strongpass1", "운영자", "", "operator", s.Now())
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	raw, err := s.Sessions.Create(context.Background(), "admin", op.ID, s.Now())
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	h := s.Handler()
	req := httptest.NewRequest("PUT", "/api/v1/a/settings/tile-key", strings.NewReader(`{"key":"X"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("operator setting tile key: status = %d, want 403", rec.Code)
	}
}

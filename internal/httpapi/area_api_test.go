package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"emergencycallup/internal/area"
)

func adminCookie(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	admin, err := s.Admins.Create(context.Background(), "admin1", "strongpass1", "관리자", "", "admin", s.Now())
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	raw, err := s.Sessions.Create(context.Background(), "admin", admin.ID, s.Now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: raw}
}

func reqJSON(t *testing.T, h http.Handler, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var r *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = bytes.NewReader(b)
	} else {
		r = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode (status %d, body %s): %v", rec.Code, rec.Body.String(), err)
	}
	return body
}

// A grid area can be created via the API and read back with its cells
// intact, in the [[i,j],...] wire format.
func TestHandleAreaCreate_Grid(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	cookie := adminCookie(t, s)

	origin := area.CellOf(250, area.Point{Lat: 35.8714, Lng: 128.6014})
	body := map[string]any{
		"name": "격자지역", "kind": "grid", "size": 250,
		"cells": [][2]int64{{origin.I, origin.J}, {origin.I + 1, origin.J}},
	}
	rec := reqJSON(t, h, http.MethodPost, "/api/v1/a/areas", body, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	created := decodeJSON(t, rec)
	id := int64(created["id"].(float64))

	rec = reqJSON(t, h, http.MethodGet, "/api/v1/a/areas", nil, cookie)
	list := decodeJSON(t, rec)
	areas := list["areas"].([]any)
	if len(areas) != 1 {
		t.Fatalf("areas count = %d, want 1", len(areas))
	}
	a := areas[0].(map[string]any)
	if int64(a["id"].(float64)) != id || a["kind"] != "grid" || a["size"].(float64) != 250 {
		t.Fatalf("area = %+v", a)
	}
	cells := a["cells"].([]any)
	if len(cells) != 2 {
		t.Fatalf("cells = %v, want 2 entries", cells)
	}
}

// Updating (PUT) an area in place must succeed while it's unused, and the
// change must actually persist (distinguishing it from the copy-on-write
// Copy* endpoints).
func TestHandleAreaUpdate_ChangesInPlace(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	cookie := adminCookie(t, s)

	rec := reqJSON(t, h, http.MethodPost, "/api/v1/a/areas",
		map[string]any{"name": "원래이름", "kind": "circle", "lat": 35.8714, "lng": 128.6014, "r": 150}, cookie)
	id := int64(decodeJSON(t, rec)["id"].(float64))

	rec = reqJSON(t, h, http.MethodPut, "/api/v1/a/areas/"+itoa(int(id)),
		map[string]any{"name": "새이름", "kind": "circle", "lat": 35.8714, "lng": 128.6014, "r": 300}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = reqJSON(t, h, http.MethodGet, "/api/v1/a/areas", nil, cookie)
	areas := decodeJSON(t, rec)["areas"].([]any)
	a := areas[0].(map[string]any)
	if a["name"] != "새이름" || a["r"].(float64) != 300 {
		t.Errorf("area after update = %+v, want name=새이름 r=300", a)
	}
}

// PUT/DELETE on an area in use by the active incident must be refused
// (409), matching docs/SPEC_AREA_EDITOR.md's R27.
func TestHandleAreaUpdateDelete_RefusedWhileInUseByActiveIncident(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	cookie := adminCookie(t, s)

	rec := reqJSON(t, h, http.MethodPost, "/api/v1/a/areas",
		map[string]any{"name": "사용중", "kind": "circle", "lat": 35.8714, "lng": 128.6014, "r": 150}, cookie)
	id := int64(decodeJSON(t, rec)["id"].(float64))

	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO incident(id, type_text, message, status, opened_by, opened_at)
		VALUES (1, '유형', '메시지', 'active', 'tester', 1000)`); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO team_task(incident_id, team_no, mission, area_id, updated_by, updated_at)
		VALUES (1, 1, '임무', ?, 'tester', 1000)`, id); err != nil {
		t.Fatalf("seed team_task: %v", err)
	}

	rec = reqJSON(t, h, http.MethodPut, "/api/v1/a/areas/"+itoa(int(id)),
		map[string]any{"name": "바꾸려함", "kind": "circle", "lat": 35.8714, "lng": 128.6014, "r": 300}, cookie)
	if rec.Code != http.StatusConflict {
		t.Errorf("update-in-use status = %d, want 409", rec.Code)
	}

	rec = reqJSON(t, h, http.MethodDelete, "/api/v1/a/areas/"+itoa(int(id)), nil, cookie)
	if rec.Code != http.StatusConflict {
		t.Errorf("delete-in-use status = %d, want 409", rec.Code)
	}
}

// DELETE succeeds (soft-delete: excluded from the active list) once unused.
func TestHandleAreaDelete_SucceedsWhenUnused(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	cookie := adminCookie(t, s)

	rec := reqJSON(t, h, http.MethodPost, "/api/v1/a/areas",
		map[string]any{"name": "지울지역", "kind": "circle", "lat": 35.8714, "lng": 128.6014, "r": 150}, cookie)
	id := int64(decodeJSON(t, rec)["id"].(float64))

	rec = reqJSON(t, h, http.MethodDelete, "/api/v1/a/areas/"+itoa(int(id)), nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = reqJSON(t, h, http.MethodGet, "/api/v1/a/areas", nil, cookie)
	areas := decodeJSON(t, rec)["areas"].([]any)
	if len(areas) != 0 {
		t.Errorf("areas after delete = %d, want 0 (active-only list)", len(areas))
	}
}

// GET /a/geo/cell must agree with the internal/area package's own CellOf
// for the same inputs (this is the server side of the client/server
// cross-check described in docs/SPEC_AREA_EDITOR.md §4.1).
func TestHandleGeoCell(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	cookie := adminCookie(t, s)

	rec := reqJSON(t, h, http.MethodGet, "/api/v1/a/geo/cell?size=250&lat=35.8714&lng=128.6014", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	if int64(body["i"].(float64)) != 46327 || int64(body["j"].(float64)) != 15972 {
		t.Errorf("cell = i=%v j=%v, want i=46327 j=15972", body["i"], body["j"])
	}
}

package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"emergencycallup/internal/geo"
)

func TestHandleGeoGeocode_Success(t *testing.T) {
	s := testServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "road" {
			t.Errorf("expected type=road, got %s", r.URL.Query().Get("type"))
		}
		w.Write([]byte(`{"response":{"status":"OK","refined":{"text":"대구광역시 중구 공평로 88"},"result":{"point":{"x":"128.601","y":"35.871"}}}}`))
	}))
	defer upstream.Close()
	s.Geo = geo.NewClient(upstream.URL)
	cookie := adminCookie(t, s)

	rec := reqJSON(t, s.Handler(), http.MethodGet, "/api/v1/a/geo/geocode?q="+"%EA%B3%B5%ED%8F%89%EB%A1%9C%2088", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	if body["lat"].(float64) != 35.871 || body["lng"].(float64) != 128.601 {
		t.Errorf("body = %+v", body)
	}
}

func TestHandleGeoGeocode_NationalPointNumberRejected(t *testing.T) {
	s := testServer(t)
	cookie := adminCookie(t, s)
	// 다사 12345678, URL-encoded.
	rec := reqJSON(t, s.Handler(), http.MethodGet, "/api/v1/a/geo/geocode?q="+"%EB%8B%A4%EC%82%AC+12345678", nil, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleGeoGeocode_NotFound(t *testing.T) {
	s := testServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":{"status":"NOT_FOUND"}}`))
	}))
	defer upstream.Close()
	s.Geo = geo.NewClient(upstream.URL)
	cookie := adminCookie(t, s)

	rec := reqJSON(t, s.Handler(), http.MethodGet, "/api/v1/a/geo/geocode?q=%EC%97%86%EB%8A%94%EC%A3%BC%EC%86%8C", nil, cookie)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleGeoAdmin_CachesResults(t *testing.T) {
	s := testServer(t)
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"response":{"status":"OK","result":{"featureCollection":{"features":[` +
			`{"type":"Feature","geometry":{"type":"Polygon","coordinates":[[[128.60,35.87],[128.61,35.87],[128.61,35.88],[128.60,35.87]]]},` +
			`"properties":{"emd_cd":"1","emd_kor_nm":"삼덕동","full_nm":"대구 중구 삼덕동"}}` +
			`]}}}}`))
	}))
	defer upstream.Close()
	s.Geo = geo.NewClient(upstream.URL)
	cookie := adminCookie(t, s)

	q := "%EC%82%BC%EB%8D%95%EB%8F%99"
	rec := reqJSON(t, s.Handler(), http.MethodGet, "/api/v1/a/geo/admin?q="+q, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	results := body["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["name"] != "삼덕동" {
		t.Fatalf("results = %+v", results)
	}

	// A second identical query must not hit the upstream again.
	reqJSON(t, s.Handler(), http.MethodGet, "/api/v1/a/geo/admin?q="+q, nil, cookie)
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1 (second query should be cache-served)", calls)
	}
}

func TestHandleGeoAdmin_UpstreamErrorMapsTo502(t *testing.T) {
	s := testServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	s.Geo = geo.NewClient(upstream.URL)
	cookie := adminCookie(t, s)

	rec := reqJSON(t, s.Handler(), http.MethodGet, "/api/v1/a/geo/admin?q=x", nil, cookie)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

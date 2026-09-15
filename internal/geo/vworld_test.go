package geo

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R29: national point number pattern is rejected without any network call.
func TestGeocode_NationalPointNumberRejected(t *testing.T) {
	c := NewClient("http://unused.invalid")
	_, err := c.Geocode(context.Background(), "key", "다사 12345678")
	if err == nil || !strings.Contains(err.Error(), "national point") {
		t.Errorf("err = %v, want ErrNationalPointNumber", err)
	}
}

// R29: road lookup fails, falls back to parcel, and succeeds.
func TestGeocode_FallsBackFromRoadToParcel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") == "road" {
			w.Write([]byte(`{"response":{"status":"NOT_FOUND"}}`))
			return
		}
		w.Write([]byte(`{"response":{"status":"OK","refined":{"text":"대구광역시 중구 공평로 88"},"result":{"point":{"x":"128.601","y":"35.871"}}}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	res, err := c.Geocode(context.Background(), "key", "공평로 88")
	if err != nil {
		t.Fatalf("geocode: %v", err)
	}
	if res.Type != "parcel" || res.Lat != 35.871 || res.Lng != 128.601 {
		t.Errorf("result = %+v, want type=parcel lat=35.871 lng=128.601", res)
	}
}

// Matches the real VWorld getcoord response shape captured against the
// live API (2026-09): road type succeeds on the first try.
func TestGeocode_RoadSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "road" {
			t.Errorf("expected type=road on first attempt, got %s", r.URL.Query().Get("type"))
		}
		w.Write([]byte(`{"response" : {"service" : {"name" : "address", "version" : "2.0", "operation" : "getcoord", "time" : "12(ms)"}, "status" : "OK", "input" : {"type" : "road", "address" : "공평로 88"}, "refined" : {"text" : "대구광역시 중구 공평로 88 (동인동1가)", "structure" : {}}, "result" : {"crs" : "EPSG:4326", "point" : {"x" : "128.60138710952316", "y" : "35.871665239495854"}}}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	res, err := c.Geocode(context.Background(), "key", "공평로 88")
	if err != nil {
		t.Fatalf("geocode: %v", err)
	}
	if res.Type != "road" || res.Matched != "대구광역시 중구 공평로 88 (동인동1가)" {
		t.Errorf("result = %+v", res)
	}
}

func TestGeocode_NeitherTypeMatches_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":{"status":"NOT_FOUND"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.Geocode(context.Background(), "key", "존재하지않는주소")
	if err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// R30: a MultiPolygon feature with 2 rings, one of them 3000 raw points,
// comes back as 2 rings, each <= 2000 points, still closed.
func TestSearchAdminDong_SimplifiesAndCaps(t *testing.T) {
	bigRing := circleRingJSON(3000, 35.87, 128.60, 0.01)
	smallRing := circleRingJSON(5, 35.90, 128.65, 0.001)

	// MultiPolygon coordinates nest 4 deep: [polygon][ring][point][lng/lat].
	// Each polygon here has just its one outer ring.
	body := `{"response":{"status":"OK","result":{"featureCollection":{"features":[` +
		`{"type":"Feature","geometry":{"type":"MultiPolygon","coordinates":[[[` + bigRing + `]],[[` + smallRing + `]]]},` +
		`"properties":{"emd_cd":"1234","emd_kor_nm":"삼덕동","full_nm":"대구광역시 중구 삼덕동"}}` +
		`]}}}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The live API rejects requests without a Referer, and rejects them
		// with a `domain` query param — pin both behaviors.
		if r.Header.Get("Referer") != "http://localhost:8080/" {
			t.Errorf("Referer = %q, want http://localhost:8080/", r.Header.Get("Referer"))
		}
		if r.URL.Query().Has("domain") {
			t.Error("request must not carry a domain= query param")
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	dongs, err := c.SearchAdminDong(context.Background(), "key", "http://localhost:8080/", "삼덕동")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(dongs) != 1 {
		t.Fatalf("dongs = %d, want 1", len(dongs))
	}
	d := dongs[0]
	if d.Name != "삼덕동" || d.Code != "1234" {
		t.Errorf("dong = %+v", d)
	}
	if len(d.Rings) != 2 {
		t.Fatalf("rings = %d, want 2", len(d.Rings))
	}
	for i, ring := range d.Rings {
		if len(ring) > maxRingPoints {
			t.Errorf("ring %d has %d points, want <= %d", i, len(ring), maxRingPoints)
		}
		if len(ring) < 2 {
			t.Errorf("ring %d has %d points, want a real shape", i, len(ring))
		}
		if ring[0] != ring[len(ring)-1] {
			t.Errorf("ring %d not closed: first=%v last=%v", i, ring[0], ring[len(ring)-1])
		}
	}
	// The big ring must actually have been reduced, not just left as-is.
	if len(d.Rings[0]) >= 3000 {
		t.Errorf("big ring has %d points, want meaningfully simplified from 3001", len(d.Rings[0]))
	}
}

// circleRingJSON writes n+1 points (closed) as a GeoJSON [[lng,lat],...]
// array tracing a circle, giving Douglas-Peucker real curvature to work
// with (a straight line would trivially collapse to 2 points regardless of
// input size, which wouldn't exercise the point-count path).
func circleRingJSON(n int, centerLat, centerLng, radiusDeg float64) string {
	var b strings.Builder
	for i := 0; i <= n; i++ {
		angle := 2 * math.Pi * float64(i) / float64(n)
		lat := centerLat + radiusDeg*math.Sin(angle)
		lng := centerLng + radiusDeg*math.Cos(angle)
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "[%f,%f]", lng, lat)
	}
	return b.String()
}

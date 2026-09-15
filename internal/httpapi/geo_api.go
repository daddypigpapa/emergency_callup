package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"emergencycallup/internal/geo"
)

type geoCacheEntry struct {
	dongs     []geo.AdminDong
	expiresAt time.Time
}

const geoCacheTTL = 10 * time.Minute

func (s *Server) baseURLHost() string {
	u, err := url.Parse(s.Cfg.BaseURL)
	if err != nil || u.Host == "" {
		return "localhost"
	}
	return u.Host
}

// handleGeoAdmin implements GET /a/geo/admin?q=<동 이름>
// (docs/SPEC_AREA_EDITOR.md §4.2): proxies VWorld's administrative-dong
// boundary search, since the browser's CSP (connect-src 'self') can't call
// VWorld directly. Results are cached in memory for geoCacheTTL — a
// best-effort convenience against repeated searches, not a correctness
// requirement, so it's never invalidated early.
func (s *Server) handleGeoAdmin(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, now, "invalid", "q 파라미터가 필요합니다.")
		return
	}

	if dongs, ok := s.geoCacheGet(q); ok {
		writeJSON(w, map[string]any{"t": now.UnixMilli(), "results": adminDongsJSON(dongs)})
		return
	}

	key := s.effectiveTileKey(r.Context())
	dongs, err := s.Geo.SearchAdminDong(r.Context(), key, s.baseURLHost(), q)
	if err != nil {
		writeError(w, now, "upstream", "행정동 정보를 가져오지 못했습니다. VWorld 키에 데이터 API 권한이 있는지 확인하세요.")
		return
	}
	s.geoCacheSet(q, dongs)
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "results": adminDongsJSON(dongs)})
}

func (s *Server) geoCacheGet(q string) ([]geo.AdminDong, bool) {
	s.geoCacheMu.Lock()
	defer s.geoCacheMu.Unlock()
	e, ok := s.geoCache[q]
	if !ok || s.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.dongs, true
}

func (s *Server) geoCacheSet(q string, dongs []geo.AdminDong) {
	s.geoCacheMu.Lock()
	defer s.geoCacheMu.Unlock()
	s.geoCache[q] = geoCacheEntry{dongs: dongs, expiresAt: s.Now().Add(geoCacheTTL)}
}

func adminDongsJSON(dongs []geo.AdminDong) []any {
	out := make([]any, 0, len(dongs))
	for _, d := range dongs {
		rings := make([][][2]float64, len(d.Rings))
		copy(rings, d.Rings)
		out = append(out, map[string]any{"code": d.Code, "name": d.Name, "full": d.Full, "rings": rings})
	}
	return out
}

// handleGeoGeocode implements GET /a/geo/geocode?q=<도로명 기초번호>
// (docs/SPEC_AREA_EDITOR.md §4.3): proxies VWorld's geocoder for rally
// point / checkpoint location entry.
func (s *Server) handleGeoGeocode(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, now, "invalid", "q 파라미터가 필요합니다.")
		return
	}

	key := s.effectiveTileKey(r.Context())
	res, err := s.Geo.Geocode(r.Context(), key, q)
	switch {
	case errors.Is(err, geo.ErrNationalPointNumber):
		writeError(w, now, "invalid", "국가지점번호는 아직 지원하지 않습니다. 도로명+기초번호(예: 공평로 88)로 입력하세요.")
	case errors.Is(err, geo.ErrNotFound):
		writeError(w, now, "not_found", "해당 기초번호를 찾지 못했습니다. 도로명과 번호를 확인하세요.")
	case err != nil:
		writeError(w, now, "upstream", "주소 조회에 실패했습니다. 잠시 후 다시 시도하세요.")
	default:
		writeJSON(w, map[string]any{
			"t": now.UnixMilli(), "lat": res.Lat, "lng": res.Lng, "matched": res.Matched, "type": res.Type,
		})
	}
}

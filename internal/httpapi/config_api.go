package httpapi

import (
	"net/http"
	"strings"
)

// handleConfig implements GET /api/v1/config: the small set of non-secret
// runtime values the frontend needs (SPEC §10.1, §10.3). No auth — nothing
// here is sensitive. TILE_KEY is already substituted into TileURL server-
// side (SPEC §10.1: "서버가 {key}를 TILE_KEY 값으로 바꿔 화면에 내려준다"),
// so the raw key itself is never sent to the browser.
//
// This exists because CSP forbids inline <script> (SPEC §11.2), so runtime
// config can't be templated into the static HTML — it's fetched instead.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	tileURL := strings.ReplaceAll(s.Cfg.TileURL, "{key}", s.Cfg.TileKey)
	writeJSON(w, map[string]any{
		"t":               now.UnixMilli(),
		"baseUrl":         s.Cfg.BaseURL,
		"tileUrl":         tileURL,
		"tileAttribution": s.Cfg.TileAttribution,
		"naverAppName":    s.Cfg.NaverAppName,
		"naverRouteType":  s.Cfg.NaverRouteType,
	})
}

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"emergencycallup/internal/settings"
)

// effectiveTileKey returns the VWorld key an admin has saved via the web UI
// (PUT /a/settings/tile-key), falling back to the TILE_KEY environment
// variable when nothing has been saved yet.
func (s *Server) effectiveTileKey(ctx context.Context) string {
	v, err := s.Settings.Get(ctx, settings.TileKeyName)
	if errors.Is(err, settings.ErrNotSet) || err != nil {
		return s.Cfg.TileKey
	}
	return v
}

// handleConfig implements GET /api/v1/config: the small set of non-secret
// runtime values the frontend needs (SPEC §10.1, §10.3). No auth — nothing
// here is sensitive.
//
// Note: the tile key IS embedded in the returned tileUrl — the browser has
// to send it on every tile request itself, so it's visible in the page's
// network traffic regardless (this matches VWorld's own key-in-URL design,
// and SPEC §10.1: "서버가 {key}를 TILE_KEY 값으로 바꿔 화면에 내려준다").
//
// This exists because CSP forbids inline <script> (SPEC §11.2), so runtime
// config can't be templated into the static HTML — it's fetched instead.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	tileURL := strings.ReplaceAll(s.Cfg.TileURL, "{key}", s.effectiveTileKey(r.Context()))
	writeJSON(w, map[string]any{
		"t":               now.UnixMilli(),
		"baseUrl":         s.Cfg.BaseURL,
		"tileUrl":         tileURL,
		"tileAttribution": s.Cfg.TileAttribution,
		"naverAppName":    s.Cfg.NaverAppName,
		"naverRouteType":  s.Cfg.NaverRouteType,
	})
}

// handleTileKeyGet implements GET /a/settings/tile-key (admin only): the
// currently effective key, so the setup page can pre-fill the input with
// whatever is actually in use (an admin override, or the TILE_KEY env var).
func (s *Server) handleTileKeyGet(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	_, err := s.Settings.Get(r.Context(), settings.TileKeyName)
	source := "env"
	if err == nil {
		source = "admin"
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "key": s.effectiveTileKey(r.Context()), "source": source})
}

type tileKeySetRequest struct {
	Key string `json:"key"`
}

// handleTileKeySet implements PUT /a/settings/tile-key (admin only): lets an
// admin enter their own VWorld key from the web UI, taking effect
// immediately (no restart) for both the field and admin screens, since both
// read the key through the same GET /api/v1/config.
func (s *Server) handleTileKeySet(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	var req tileKeySetRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.Key) == "" {
		writeError(w, now, "invalid", "키 값을 입력하세요.")
		return
	}
	key := strings.TrimSpace(req.Key)
	if err := s.Settings.Set(r.Context(), settings.TileKeyName, key, "admin:"+admin.LoginID, now); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "key": key})
}

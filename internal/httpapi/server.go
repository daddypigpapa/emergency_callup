// Package httpapi wires together config, storage and the domain packages
// into the HTTP server described in docs/SPEC.md §7.
package httpapi

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"sync"
	"time"

	"emergencycallup/internal/area"
	"emergencycallup/internal/auth"
	"emergencycallup/internal/config"
	"emergencycallup/internal/geo"
	"emergencycallup/internal/incident"
	"emergencycallup/internal/roster"
	"emergencycallup/internal/settings"
	"emergencycallup/internal/sms"
	"emergencycallup/internal/store"
	"emergencycallup/internal/tracker"

	"emergencycallup/internal/audit"
)

// Server holds every dependency handlers need. Constructed once at startup.
type Server struct {
	Cfg      *config.Config
	DB       *store.DB
	Hasher   *auth.Hasher
	Sessions *auth.SessionStore
	Admins   *auth.AdminStore
	Limiter  *auth.LoginLimiter
	Audit    *audit.Log

	Areas     *area.Store
	Members   *roster.Store
	Importer  *roster.Importer
	Presets   *roster.PresetStore
	Incidents *incident.Store
	Tracker   *tracker.Tracker
	SMS       *sms.Service
	Settings  *settings.Store
	Plans     *incident.PlanStore
	Geo       *geo.Client

	// geoCache holds GET /a/geo/admin results for 10 minutes
	// (docs/SPEC_AREA_EDITOR.md §4.2) — best-effort, not persisted.
	geoCacheMu sync.Mutex
	geoCache   map[string]geoCacheEntry

	// Now is injected so tests can control the clock. Defaults to time.Now.
	Now func() time.Time

	// BackupHook, if set, is invoked (in a new goroutine) right after an
	// incident closes (SPEC §12.4: "사건 종료 직후" backup). cmd/server
	// wires this to store.DB.Backup.
	BackupHook func(context.Context)

	// startedAt records process start for uptime/health reporting.
	startedAt time.Time
}

// NewServer builds a Server from a config and open database. It wires every
// domain package together but does not load tracker state from the DB or
// start the batch-flush loop — call s.Tracker.LoadActive(ctx) and start a
// flush ticker from cmd/server after construction.
func NewServer(cfg *config.Config, db *store.DB) *Server {
	hasher := auth.NewHasher()
	auditLog := audit.New(db.DB)
	areas := area.NewStore(db.DB)
	members := roster.NewStore(db.DB, hasher)
	plans := incident.NewPlanStore(db.DB)
	incidents := incident.NewStore(db.DB, auditLog, areas, plans)

	var httpProvider sms.Provider
	if cfg.SMSHTTPEnabled() {
		p, err := sms.NewHTTPProvider(cfg.SMSHTTPURL, cfg.SMSHTTPAuthHeader, cfg.SMSHTTPBodyTemplate, cfg.SMSSender, cfg.SMSHTTPRPS)
		if err != nil {
			log.Printf("sms: invalid SMS_HTTP_BODY_TEMPLATE, http provider disabled: %v", err)
		} else {
			httpProvider = p
		}
	}

	return &Server{
		Cfg:       cfg,
		DB:        db,
		Hasher:    hasher,
		Sessions:  auth.NewSessionStore(db.DB),
		Admins:    auth.NewAdminStore(db.DB, hasher),
		Limiter:   auth.NewLoginLimiter(),
		Audit:     auditLog,
		Areas:     areas,
		Members:   members,
		Importer:  roster.NewImporter(db.DB, areas, members),
		Presets:   roster.NewPresetStore(db.DB),
		Incidents: incidents,
		Tracker:   tracker.New(db.DB, incidents, areas, auditLog),
		SMS:       sms.NewService(db.DB, httpProvider, auditLog),
		Settings:  settings.NewStore(db.DB),
		Plans:     plans,
		Geo:       geo.NewClient(""),
		geoCache:  map[string]geoCacheEntry{},
		Now:       time.Now,
		startedAt: time.Now(),
	}
}

// sessionCookieName is the single cookie this system uses (SPEC §2.1: "쿠키는
// 1개만").
const sessionCookieName = "sid"

// Handler builds the full route tree with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/v1/config", s.handleConfig)

	mux.HandleFunc("POST /api/v1/a/login", s.handleAdminLogin)
	mux.HandleFunc("POST /api/v1/a/logout", s.handleAdminLogout)

	// Field personnel (SPEC §7.2)
	mux.HandleFunc("POST /api/v1/f/login", s.handleMemberLogin)
	mux.HandleFunc("POST /api/v1/f/password", s.requireMemberSession(s.handleMemberPassword))
	mux.HandleFunc("POST /api/v1/f/logout", s.handleMemberLogout)
	mux.HandleFunc("GET /api/v1/f/me", s.requireMemberSession(s.handleMemberMe))
	mux.HandleFunc("POST /api/v1/f/consent", s.requireMemberSession(s.handleMemberConsent))
	mux.HandleFunc("POST /api/v1/f/fix", s.requireMemberSession(s.handleMemberFix))
	mux.HandleFunc("GET /api/v1/f/sync", s.requireMemberSession(s.handleMemberSync))
	mux.HandleFunc("POST /api/v1/f/ack", s.requireMemberSession(s.handleMemberAck))

	// Admin situation board (SPEC §7.3)
	mux.HandleFunc("GET /api/v1/a/snapshot", s.requireAdminSession(auth.RoleOperator, s.handleSnapshot))
	mux.HandleFunc("GET /api/v1/a/delta", s.requireAdminSession(auth.RoleOperator, s.handleDelta))
	mux.HandleFunc("GET /api/v1/a/incidents/draft", s.requireAdminSession(auth.RoleOperator, s.handleIncidentDraft))
	mux.HandleFunc("POST /api/v1/a/incidents", s.requireAdminSession(auth.RoleOperator, s.handleIncidentOpen))
	mux.HandleFunc("PATCH /api/v1/a/incidents/current", s.requireAdminSession(auth.RoleOperator, s.handleIncidentUpdateMeta))
	mux.HandleFunc("PUT /api/v1/a/incidents/current/teams/{no}", s.requireAdminSession(auth.RoleOperator, s.handleTeamTaskUpdate))
	mux.HandleFunc("POST /api/v1/a/incidents/current/members", s.requireAdminSession(auth.RoleOperator, s.handleIncidentAddMember))
	mux.HandleFunc("PUT /api/v1/a/incidents/current/members/{id}", s.requireAdminSession(auth.RoleOperator, s.handleMemberAssignmentUpdate))
	mux.HandleFunc("GET /api/v1/a/incidents/current/members/{id}/contact", s.requireAdminSession(auth.RoleOperator, s.handleMemberContact))
	mux.HandleFunc("POST /api/v1/a/incidents/current/close", s.requireAdminSession(auth.RoleOperator, s.handleIncidentClose))
	mux.HandleFunc("POST /api/v1/a/incidents/{id}/sms", s.requireAdminSession(auth.RoleOperator, s.handleSMSPrepare))
	mux.HandleFunc("GET /api/v1/a/incidents/{id}/sms/{batchId}/recipients.csv", s.requireAdminSession(auth.RoleOperator, s.handleSMSRecipientsCSV))
	mux.HandleFunc("POST /api/v1/a/incidents/{id}/sms/{batchId}/mark-sent", s.requireAdminSession(auth.RoleOperator, s.handleSMSMarkSent))
	mux.HandleFunc("GET /api/v1/a/incidents/{id}/sms/{batchId}", s.requireAdminSession(auth.RoleOperator, s.handleSMSBatchResult))
	mux.HandleFunc("GET /api/v1/a/incidents/{id}/report.csv", s.requireAdminSession(auth.RoleOperator, s.handleReportCSV))

	// Admin setup (admin role only)
	mux.HandleFunc("GET /api/v1/a/members", s.requireAdminSession(auth.RoleAdmin, s.handleMembersList))
	mux.HandleFunc("POST /api/v1/a/members", s.requireAdminSession(auth.RoleAdmin, s.handleMemberCreate))
	mux.HandleFunc("PUT /api/v1/a/members/{id}", s.requireAdminSession(auth.RoleAdmin, s.handleMemberUpdate))
	mux.HandleFunc("POST /api/v1/a/members/import", s.requireAdminSession(auth.RoleAdmin, s.handleMemberImport))
	mux.HandleFunc("POST /api/v1/a/members/{id}/password-reset", s.requireAdminSession(auth.RoleAdmin, s.handleMemberPasswordReset))
	mux.HandleFunc("GET /api/v1/a/areas", s.requireAdminSession(auth.RoleAdmin, s.handleAreasList))
	mux.HandleFunc("POST /api/v1/a/areas", s.requireAdminSession(auth.RoleAdmin, s.handleAreaCreate))
	mux.HandleFunc("PUT /api/v1/a/areas/{id}", s.requireAdminSession(auth.RoleAdmin, s.handleAreaUpdate))
	mux.HandleFunc("DELETE /api/v1/a/areas/{id}", s.requireAdminSession(auth.RoleAdmin, s.handleAreaDelete))
	mux.HandleFunc("POST /api/v1/a/areas/{id}/copy", s.requireAdminSession(auth.RoleAdmin, s.handleAreaCopy))
	mux.HandleFunc("GET /api/v1/a/geo/cell", s.requireAdminSession(auth.RoleAdmin, s.handleGeoCell))
	mux.HandleFunc("GET /api/v1/a/geo/admin", s.requireAdminSession(auth.RoleAdmin, s.handleGeoAdmin))
	mux.HandleFunc("GET /api/v1/a/geo/geocode", s.requireAdminSession(auth.RoleAdmin, s.handleGeoGeocode))
	mux.HandleFunc("GET /api/v1/a/team-plans", s.requireAdminSession(auth.RoleOperator, s.handleTeamPlansList))
	mux.HandleFunc("PUT /api/v1/a/team-plans", s.requireAdminSession(auth.RoleAdmin, s.handleTeamPlansSave))
	mux.HandleFunc("GET /api/v1/a/presets", s.requireAdminSession(auth.RoleAdmin, s.handlePresetsList))
	mux.HandleFunc("POST /api/v1/a/presets", s.requireAdminSession(auth.RoleAdmin, s.handlePresetCreate))
	mux.HandleFunc("PUT /api/v1/a/presets/{id}", s.requireAdminSession(auth.RoleAdmin, s.handlePresetUpdate))
	mux.HandleFunc("DELETE /api/v1/a/presets/{id}", s.requireAdminSession(auth.RoleAdmin, s.handlePresetDelete))
	mux.HandleFunc("GET /api/v1/a/admins", s.requireAdminSession(auth.RoleAdmin, s.handleAdminsList))
	mux.HandleFunc("POST /api/v1/a/admins", s.requireAdminSession(auth.RoleAdmin, s.handleAdminCreate))
	mux.HandleFunc("PUT /api/v1/a/admins/{id}", s.requireAdminSession(auth.RoleAdmin, s.handleAdminUpdate))
	mux.HandleFunc("GET /api/v1/a/events", s.requireAdminSession(auth.RoleAdmin, s.handleEventsList))
	mux.HandleFunc("GET /api/v1/a/settings/tile-key", s.requireAdminSession(auth.RoleAdmin, s.handleTileKeyGet))
	mux.HandleFunc("PUT /api/v1/a/settings/tile-key", s.requireAdminSession(auth.RoleAdmin, s.handleTileKeySet))

	s.registerStatic(mux)

	tileOrigin := tileOriginFrom(s.Cfg.TileURL)
	hsts := s.Cfg.TLSEnabled()

	return chain(mux,
		gzipResponses,
		securityHeaders(tileOrigin, hsts),
		requireJSONContentType,
	)
}

// handleHealthz reports basic liveness (SPEC §12.4). No auth, no personal
// data.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	dbOK := "ok"
	if err := s.DB.PingContext(r.Context()); err != nil {
		dbOK = "error"
	}

	var activeIncidentID *int64
	var id int64
	err := s.DB.QueryRowContext(r.Context(), `SELECT id FROM incident WHERE status = 'active'`).Scan(&id)
	if err == nil {
		activeIncidentID = &id
	} else if err != sql.ErrNoRows {
		dbOK = "error"
	}

	writeJSON(w, map[string]any{
		"ok":             dbOK == "ok",
		"db":             dbOK,
		"lastFlushAgoMs": 0, // wired up once the tracker's batch writer exists (Phase 4)
		"activeIncident": activeIncidentID,
		"t":              now.UnixMilli(),
	})
}

// getToken extracts the session token from the cookie or, failing that, a
// Bearer header (SPEC §7.1: "쿠키 sid ... 또는 Authorization: Bearer").
func getToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) > len(prefix) && auth[:len(prefix)] == prefix {
		return auth[len(prefix):]
	}
	return ""
}

func setSessionCookie(w http.ResponseWriter, raw string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(maxAge.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// clientIP returns the request's source IP, honoring X-Forwarded-For only
// when TRUST_PROXY is enabled (SPEC §12.3).
func (s *Server) clientIP(r *http.Request) string {
	if s.Cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return xff
		}
	}
	return r.RemoteAddr
}

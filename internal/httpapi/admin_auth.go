package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"emergencycallup/internal/audit"
	"emergencycallup/internal/auth"
)

type adminLoginRequest struct {
	ID string `json:"id"`
	PW string `json:"pw"`
}

// handleAdminLogin implements POST /a/login (SPEC §7.3).
func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	ip := s.clientIP(r)

	if !s.Limiter.AllowIP(ip, now) {
		writeErrorRetry(w, now, "rate", "요청이 너무 많습니다. 잠시 후 다시 시도하세요.", 60)
		return
	}

	var req adminLoginRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.ID == "" || req.PW == "" {
		writeError(w, now, "invalid", "아이디와 비밀번호를 입력하세요.")
		return
	}

	if wait := s.Limiter.LoginBackoff(req.ID, now); wait > 0 {
		writeErrorRetry(w, now, "rate", "너무 많이 실패했습니다. 잠시 후 다시 시도하세요.", int(wait.Seconds())+1)
		return
	}

	fail := func(reason string) {
		s.Limiter.RecordFailure(req.ID, now)
		_ = s.Audit.Record(r.Context(), now, audit.Event{
			Type:  "login_fail",
			Actor: audit.ActorAdmin(req.ID),
			Data:  map[string]string{"reason": reason},
		})
		writeError(w, now, "auth", "아이디 또는 비밀번호가 올바르지 않습니다.")
	}

	admin, err := s.Admins.FindByLoginID(r.Context(), req.ID)
	if err != nil {
		if errors.Is(err, auth.ErrAdminNotFound) {
			fail("no_such_id")
			return
		}
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	if !admin.Active {
		fail("inactive")
		return
	}
	ok, err := s.Hasher.Verify(admin.PWHash, req.PW)
	if err != nil {
		if errors.Is(err, auth.ErrHashBusy) {
			writeErrorRetry(w, now, "busy", "서버가 바쁩니다. 잠시 후 다시 시도하세요.", 5)
			return
		}
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	if !ok {
		fail("bad_password")
		return
	}

	s.Limiter.RecordSuccess(req.ID)
	raw, err := s.Sessions.Create(r.Context(), auth.KindAdmin, admin.ID, now)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "login_ok", Actor: audit.ActorAdmin(req.ID)})

	setSessionCookie(w, raw, auth.AdminSessionTTL)
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "name": admin.Name, "role": admin.Role})
}

// handleAdminLogout implements POST /a/logout.
func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	if tok := getToken(r); tok != "" {
		_ = s.Sessions.Revoke(r.Context(), tok)
	}
	clearSessionCookie(w)
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

type ctxKey int

const ctxKeyAdmin ctxKey = iota

// requireAdminSession resolves the session cookie/bearer token to an admin
// account and, if minRole is auth.RoleAdmin, additionally checks the role
// (SPEC §7.3: operator vs admin). Any failure is a 401, never a 500
// (SPEC §7.1: "잘못된 쿠키·토큰은 401").
func (s *Server) requireAdminSession(minRole string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := s.Now()
		tok := getToken(r)
		if tok == "" {
			writeError(w, now, "auth", "로그인이 필요합니다.")
			return
		}
		sess, err := s.Sessions.Validate(r.Context(), tok, now)
		if err != nil || sess.Kind != auth.KindAdmin {
			writeError(w, now, "auth", "로그인이 필요합니다.")
			return
		}
		admin, err := s.Admins.FindByID(r.Context(), sess.SubjectID)
		if err != nil || !admin.Active {
			writeError(w, now, "auth", "로그인이 필요합니다.")
			return
		}
		if minRole == auth.RoleAdmin && admin.Role != auth.RoleAdmin {
			writeError(w, now, "forbidden", "권한이 없습니다.")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyAdmin, admin)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

func adminFromContext(ctx context.Context) *auth.AdminUser {
	u, _ := ctx.Value(ctxKeyAdmin).(*auth.AdminUser)
	return u
}

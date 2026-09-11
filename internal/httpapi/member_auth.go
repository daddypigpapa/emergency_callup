package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"emergencycallup/internal/audit"
	"emergencycallup/internal/auth"
	"emergencycallup/internal/roster"
)

type memberLoginRequest struct {
	ID string `json:"id"`
	PW string `json:"pw"`
}

// handleMemberLogin implements POST /f/login (SPEC §7.2, §8.1.1).
func (s *Server) handleMemberLogin(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	ip := s.clientIP(r)
	if !s.Limiter.AllowIP(ip, now) {
		writeErrorRetry(w, now, "rate", "요청이 너무 많습니다. 잠시 후 다시 시도하세요.", 60)
		return
	}

	var req memberLoginRequest
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
		_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "login_fail", Actor: audit.ActorMember(0), Data: map[string]string{"loginId": req.ID, "reason": reason}})
		writeError(w, now, "auth", "아이디 또는 비밀번호가 올바르지 않습니다.")
	}

	m, err := s.Members.GetByLoginID(r.Context(), req.ID)
	if err != nil {
		if errors.Is(err, roster.ErrNotFound) {
			fail("no_such_id")
			return
		}
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	if !m.Active {
		fail("inactive")
		return
	}
	ok, err := s.Hasher.Verify(m.PWHash, req.PW)
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
	raw, err := s.Sessions.Create(r.Context(), auth.KindMember, m.ID, now)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "login_ok", Actor: audit.ActorMember(m.ID), MemberID: &m.ID})

	setSessionCookie(w, raw, auth.MemberSessionTTL)
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "mustChange": m.PWMustChange})
}

type memberPasswordRequest struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// handleMemberPassword implements POST /f/password.
func (s *Server) handleMemberPassword(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	m := memberFromContext(r.Context())
	var req memberPasswordRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Old == "" || req.New == "" {
		writeError(w, now, "invalid", "현재/새 비밀번호를 입력하세요.")
		return
	}
	ok, err := s.Hasher.Verify(m.PWHash, req.Old)
	if err != nil {
		if errors.Is(err, auth.ErrHashBusy) {
			writeErrorRetry(w, now, "busy", "서버가 바쁩니다.", 5)
			return
		}
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	if !ok {
		writeError(w, now, "auth", "현재 비밀번호가 올바르지 않습니다.")
		return
	}
	if err := s.Members.SetPassword(r.Context(), m.ID, req.New, false); err != nil {
		if errors.Is(err, auth.ErrPasswordTooShort) || errors.Is(err, auth.ErrPasswordHasLoginID) || errors.Is(err, auth.ErrPasswordHasMobile) {
			writeError(w, now, "invalid", "비밀번호 정책을 만족하지 않습니다.")
			return
		}
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

func (s *Server) handleMemberLogout(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	if tok := getToken(r); tok != "" {
		_ = s.Sessions.Revoke(r.Context(), tok)
	}
	clearSessionCookie(w)
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

type ctxKeyMember struct{}

// requireMemberSession resolves the session to an active member account.
// Any failure is 401 (SPEC §7.1).
func (s *Server) requireMemberSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := s.Now()
		tok := getToken(r)
		if tok == "" {
			writeError(w, now, "auth", "로그인이 필요합니다.")
			return
		}
		sess, err := s.Sessions.Validate(r.Context(), tok, now)
		if err != nil || sess.Kind != auth.KindMember {
			writeError(w, now, "auth", "로그인이 필요합니다.")
			return
		}
		m, err := s.Members.GetByID(r.Context(), sess.SubjectID)
		if err != nil || !m.Active {
			writeError(w, now, "auth", "로그인이 필요합니다.")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyMember{}, m)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

func memberFromContext(ctx context.Context) *roster.Member {
	m, _ := ctx.Value(ctxKeyMember{}).(*roster.Member)
	return m
}

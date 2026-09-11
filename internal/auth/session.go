package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

const (
	KindAdmin  = "admin"
	KindMember = "member"

	// SPEC §11.1: "관리자 12시간(무활동 2시간)"
	AdminSessionTTL  = 12 * time.Hour
	AdminIdleTimeout = 2 * time.Hour

	// SPEC §11.1 / §16-1 (unresolved item 1, default kept):
	// "현장인력 24시간 또는 사건 종료 1시간 후 중 빠른 쪽"
	// DECISION: default = 24h absolute cap; the "closed+1h" shortening is
	// applied by incident-close handling via ExpireMemberSessionsBy, not
	// baked in here (this package has no incident knowledge).
	MemberSessionTTL = 24 * time.Hour
)

var (
	ErrSessionNotFound = errors.New("auth: session not found")
	ErrSessionExpired  = errors.New("auth: session expired")
)

// Session mirrors the `session` table (SPEC §4).
type Session struct {
	TokenHash  string
	Kind       string
	SubjectID  int64
	CreatedAt  int64
	ExpiresAt  int64
	LastSeenAt int64
}

// SessionStore manages the `session` table.
type SessionStore struct {
	db *sql.DB
}

func NewSessionStore(db *sql.DB) *SessionStore { return &SessionStore{db: db} }

// newRawToken returns a URL-safe random token (32 bytes of entropy) and its
// SHA-256 hex hash, per SPEC §11.1 ("토큰 32바이트 난수, DB엔 SHA-256만").
func newRawToken() (raw string, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	hash = HashToken(raw)
	return raw, hash, nil
}

// HashToken returns the hex-encoded SHA-256 hash of a raw session token.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Create issues a new session for the given kind/subject and returns the raw
// token to hand to the client (cookie or Bearer value). now is injected for
// testability.
func (s *SessionStore) Create(ctx context.Context, kind string, subjectID int64, now time.Time) (rawToken string, err error) {
	var ttl time.Duration
	switch kind {
	case KindAdmin:
		ttl = AdminSessionTTL
	case KindMember:
		ttl = MemberSessionTTL
	default:
		return "", errors.New("auth: unknown session kind")
	}
	raw, hash, err := newRawToken()
	if err != nil {
		return "", err
	}
	nowMs := now.UnixMilli()
	expMs := now.Add(ttl).UnixMilli()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO session(token_hash, kind, subject_id, created_at, expires_at, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hash, kind, subjectID, nowMs, expMs, nowMs)
	if err != nil {
		return "", err
	}
	return raw, nil
}

// Validate looks up a session by raw token, enforces absolute expiry and (for
// admin sessions) the idle timeout, and — if still valid — bumps
// last_seen_at. An invalid/expired/missing token must map to HTTP 401
// (SPEC §7.1: "잘못된 쿠키·토큰은 401").
func (s *SessionStore) Validate(ctx context.Context, rawToken string, now time.Time) (*Session, error) {
	hash := HashToken(rawToken)
	row := s.db.QueryRowContext(ctx,
		`SELECT token_hash, kind, subject_id, created_at, expires_at, last_seen_at
		 FROM session WHERE token_hash = ?`, hash)

	var sess Session
	if err := row.Scan(&sess.TokenHash, &sess.Kind, &sess.SubjectID, &sess.CreatedAt, &sess.ExpiresAt, &sess.LastSeenAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}

	nowMs := now.UnixMilli()
	if nowMs >= sess.ExpiresAt {
		_ = s.revokeByHash(ctx, hash)
		return nil, ErrSessionExpired
	}
	if sess.Kind == KindAdmin {
		idleMs := AdminIdleTimeout.Milliseconds()
		if nowMs-sess.LastSeenAt > idleMs {
			_ = s.revokeByHash(ctx, hash)
			return nil, ErrSessionExpired
		}
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE session SET last_seen_at = ? WHERE token_hash = ?`, nowMs, hash); err != nil {
		return nil, err
	}
	sess.LastSeenAt = nowMs
	return &sess, nil
}

// Revoke deletes a single session by its raw token (logout).
func (s *SessionStore) Revoke(ctx context.Context, rawToken string) error {
	return s.revokeByHash(ctx, HashToken(rawToken))
}

func (s *SessionStore) revokeByHash(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM session WHERE token_hash = ?`, hash)
	return err
}

// RevokeAllForSubject deletes every session for a given kind+subject, used on
// logout-everywhere, password change/reset, and deactivation
// (SPEC §11.1 "세션 폐기").
func (s *SessionStore) RevokeAllForSubject(ctx context.Context, kind string, subjectID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM session WHERE kind = ? AND subject_id = ?`, kind, subjectID)
	return err
}

// ExpireMemberSessionsBy caps every member session's expiry to at most
// cutoffMs, implementing "사건 종료 1시간 후" (SPEC §11.1, §16-1). It never
// extends a session — only shortens sessions that would otherwise outlive
// the cutoff. Called by the incident package when an incident closes.
func (s *SessionStore) ExpireMemberSessionsBy(ctx context.Context, cutoffMs int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE session SET expires_at = MIN(expires_at, ?) WHERE kind = 'member' AND expires_at > ?`,
		cutoffMs, cutoffMs)
	return err
}

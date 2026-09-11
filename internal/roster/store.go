package roster

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"emergencycallup/internal/auth"
)

var (
	ErrNotFound        = errors.New("roster: member not found")
	ErrLoginIDTaken    = errors.New("roster: login ID already in use")
	ErrMobileTaken     = errors.New("roster: mobile number already in use")
	ErrTeamNotFound    = errors.New("roster: team does not exist")
	ErrAreaNameUnknown = errors.New("roster: area name does not match any active area")
)

// Member mirrors the `member` table (SPEC §4). AreaID/Mission/Note carry
// the peacetime defaults used by SPEC §5.4's per-incident auto-fill.
type Member struct {
	ID           int64
	LoginID      string
	PWHash       string
	PWMustChange bool
	Dept         string
	Name         string
	OfficeTel    string // "" if none
	Mobile       string
	TeamNo       int64 // 0 = unset
	Mission      string
	AreaID       int64 // 0 = unset
	Note         string
	Active       bool
	CreatedAt    int64
	UpdatedAt    int64
}

// Store manages the `member` table.
type Store struct {
	db *sql.DB
	h  *auth.Hasher
}

func NewStore(db *sql.DB, h *auth.Hasher) *Store { return &Store{db: db, h: h} }

// NewInput carries the fields needed to register one member (= issue an
// account), already normalized by the caller (SPEC §4.2).
type NewInput struct {
	LoginID   string
	Dept      string
	Name      string
	OfficeTel string
	Mobile    string
	TeamNo    int64
	Mission   string
	AreaID    int64 // 0 = none
	Note      string
}

// Create registers a new member and issues a random one-time password
// (SPEC §11.1: "무작위 10자... 등록·초기화 응답에서 1회만 표시").
func (s *Store) Create(ctx context.Context, in NewInput, now time.Time) (member *Member, plaintextPassword string, err error) {
	pw, err := auth.GenerateRandomPassword()
	if err != nil {
		return nil, "", err
	}
	hash, err := s.h.Hash(pw)
	if err != nil {
		return nil, "", err
	}
	nowMs := now.UnixMilli()
	var areaID any
	if in.AreaID != 0 {
		areaID = in.AreaID
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO member(login_id, pw_hash, pw_must_change, dept, name, office_tel, mobile, team_no, mission, area_id, note, active, created_at, updated_at)
		 VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		in.LoginID, hash, in.Dept, in.Name, nullIfEmpty(in.OfficeTel), in.Mobile, in.TeamNo, nullIfEmpty(in.Mission), areaID, nullIfEmpty(in.Note), nowMs, nowMs)
	if err != nil {
		return nil, "", mapConstraintErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, "", err
	}
	m, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, "", err
	}
	return m, pw, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func mapConstraintErr(err error) error {
	msg := err.Error()
	if containsSub(msg, "member.login_id") {
		return ErrLoginIDTaken
	}
	if containsSub(msg, "member.mobile") {
		return ErrMobileTaken
	}
	if containsSub(msg, "UNIQUE constraint failed") {
		return ErrLoginIDTaken // best-effort fallback
	}
	return err
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

const memberCols = `id, login_id, pw_hash, pw_must_change, dept, name, COALESCE(office_tel,''), mobile,
	COALESCE(team_no,0), COALESCE(mission,''), COALESCE(area_id,0), COALESCE(note,''), active, created_at, updated_at`

func scanMember(row interface{ Scan(...any) error }) (*Member, error) {
	var m Member
	var mustChange, active int
	if err := row.Scan(&m.ID, &m.LoginID, &m.PWHash, &mustChange, &m.Dept, &m.Name, &m.OfficeTel, &m.Mobile,
		&m.TeamNo, &m.Mission, &m.AreaID, &m.Note, &active, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.PWMustChange = mustChange != 0
	m.Active = active != 0
	return &m, nil
}

func (s *Store) GetByID(ctx context.Context, id int64) (*Member, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+memberCols+` FROM member WHERE id = ?`, id)
	m, err := scanMember(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

func (s *Store) GetByLoginID(ctx context.Context, loginID string) (*Member, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+memberCols+` FROM member WHERE login_id = ?`, loginID)
	m, err := scanMember(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// GetByMobile is used by xlsx import to decide new-vs-changed (SPEC R4: the
// mobile number is the identity key across re-imports of the same person).
func (s *Store) GetByMobile(ctx context.Context, mobile string) (*Member, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+memberCols+` FROM member WHERE mobile = ?`, mobile)
	m, err := scanMember(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// List returns members ordered by team then name.
func (s *Store) List(ctx context.Context, activeOnly bool) ([]Member, error) {
	q := `SELECT ` + memberCols + ` FROM member`
	if activeOnly {
		q += ` WHERE active = 1`
	}
	q += ` ORDER BY team_no, name`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// UpdateInput carries editable fields for PUT /a/members/{id}. Pointer
// fields are optional partial updates; nil = leave unchanged.
type UpdateInput struct {
	Dept      *string
	Name      *string
	OfficeTel *string
	Mobile    *string
	TeamNo    *int64
	Mission   *string
	AreaID    *int64
	Note      *string
}

// Update applies a partial update to a member's roster fields (not
// credentials — see SetPassword/PasswordReset).
func (s *Store) Update(ctx context.Context, id int64, in UpdateInput, now time.Time) error {
	m, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	dept, name, officeTel, mobile, mission, note := m.Dept, m.Name, m.OfficeTel, m.Mobile, m.Mission, m.Note
	teamNo, areaID := m.TeamNo, m.AreaID
	if in.Dept != nil {
		dept = *in.Dept
	}
	if in.Name != nil {
		name = *in.Name
	}
	if in.OfficeTel != nil {
		officeTel = *in.OfficeTel
	}
	if in.Mobile != nil {
		mobile = *in.Mobile
	}
	if in.TeamNo != nil {
		teamNo = *in.TeamNo
	}
	if in.Mission != nil {
		mission = *in.Mission
	}
	if in.AreaID != nil {
		areaID = *in.AreaID
	}
	if in.Note != nil {
		note = *in.Note
	}
	var areaVal, teamVal any
	if areaID != 0 {
		areaVal = areaID
	}
	if teamNo != 0 {
		teamVal = teamNo
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE member SET dept=?, name=?, office_tel=?, mobile=?, team_no=?, mission=?, area_id=?, note=?, updated_at=?
		 WHERE id=?`,
		dept, name, nullIfEmpty(officeTel), mobile, teamVal, nullIfEmpty(mission), areaVal, nullIfEmpty(note), now.UnixMilli(), id)
	if err != nil {
		return mapConstraintErr(err)
	}
	return nil
}

// SetActive activates/deactivates a member. Session revocation on
// deactivation is the caller's job (needs auth.SessionStore) — SPEC §11.1
// "비활성화 즉시 세션 폐기" is enforced at the httpapi handler layer so this
// package doesn't need to depend on auth.SessionStore.
func (s *Store) SetActive(ctx context.Context, id int64, active bool, now time.Time) error {
	v := 0
	if active {
		v = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE member SET active=?, updated_at=? WHERE id=?`, v, now.UnixMilli(), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPassword hashes and stores a new password, clearing pw_must_change
// only when explicitly requested (self-service password change clears it;
// an admin reset sets it, per SPEC §7.3).
func (s *Store) SetPassword(ctx context.Context, id int64, newPassword string, mustChange bool) error {
	m, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := auth.ValidatePasswordPolicy(newPassword, m.LoginID, m.Mobile); err != nil {
		return err
	}
	hash, err := s.h.Hash(newPassword)
	if err != nil {
		return err
	}
	mc := 0
	if mustChange {
		mc = 1
	}
	_, err = s.db.ExecContext(ctx, `UPDATE member SET pw_hash=?, pw_must_change=? WHERE id=?`, hash, mc, id)
	return err
}

// PasswordReset issues a new random password (admin-initiated), returned
// once in plaintext (SPEC §7.3 "임시 비밀번호 1회 표시").
func (s *Store) PasswordReset(ctx context.Context, id int64) (plaintext string, err error) {
	pw, err := auth.GenerateRandomPassword()
	if err != nil {
		return "", err
	}
	hash, err := s.h.Hash(pw)
	if err != nil {
		return "", err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE member SET pw_hash=?, pw_must_change=1 WHERE id=?`, hash, id)
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", ErrNotFound
	}
	return pw, nil
}

package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AdminRole values, matching the `admin_user.role` CHECK constraint.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
)

var (
	ErrAdminNotFound     = errors.New("auth: admin not found")
	ErrAdminLoginIDTaken = errors.New("auth: login ID already in use")
	ErrLastActiveAdmin   = errors.New("auth: cannot deactivate the last active admin")
	ErrInvalidRole       = errors.New("auth: role must be admin or operator")
)

// AdminUser mirrors the `admin_user` table (SPEC §4).
type AdminUser struct {
	ID        int64
	LoginID   string
	PWHash    string
	Name      string
	Dept      string
	Role      string
	Active    bool
	CreatedAt int64
}

// AdminStore manages the `admin_user` table.
type AdminStore struct {
	db *sql.DB
	h  *Hasher
}

func NewAdminStore(db *sql.DB, h *Hasher) *AdminStore { return &AdminStore{db: db, h: h} }

// Create inserts a new admin account. SPEC §11.1: "기본 계정·비밀번호 없음" —
// the caller (cmd/server "admin create") always supplies an explicit
// password; there is no default.
func (s *AdminStore) Create(ctx context.Context, loginID, password, name, dept, role string, now time.Time) (*AdminUser, error) {
	if role != RoleAdmin && role != RoleOperator {
		return nil, ErrInvalidRole
	}
	if err := ValidatePasswordPolicy(password, loginID, ""); err != nil {
		return nil, err
	}
	hash, err := s.h.Hash(password)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO admin_user(login_id, pw_hash, name, dept, role, active, created_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?)`,
		loginID, hash, name, dept, role, now.UnixMilli())
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, ErrAdminLoginIDTaken
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &AdminUser{ID: id, LoginID: loginID, PWHash: hash, Name: name, Dept: dept, Role: role, Active: true, CreatedAt: now.UnixMilli()}, nil
}

// FindByLoginID returns an admin by login ID regardless of active state
// (callers must check Active themselves — SPEC §11.1 login flow rejects
// inactive accounts explicitly so the audit trail records login_fail).
func (s *AdminStore) FindByLoginID(ctx context.Context, loginID string) (*AdminUser, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, login_id, pw_hash, name, COALESCE(dept,''), role, active, created_at
		 FROM admin_user WHERE login_id = ?`, loginID)
	var u AdminUser
	var active int
	if err := row.Scan(&u.ID, &u.LoginID, &u.PWHash, &u.Name, &u.Dept, &u.Role, &active, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAdminNotFound
		}
		return nil, err
	}
	u.Active = active != 0
	return &u, nil
}

// FindByID returns an admin by primary key.
func (s *AdminStore) FindByID(ctx context.Context, id int64) (*AdminUser, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, login_id, pw_hash, name, COALESCE(dept,''), role, active, created_at
		 FROM admin_user WHERE id = ?`, id)
	var u AdminUser
	var active int
	if err := row.Scan(&u.ID, &u.LoginID, &u.PWHash, &u.Name, &u.Dept, &u.Role, &active, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAdminNotFound
		}
		return nil, err
	}
	u.Active = active != 0
	return &u, nil
}

// List returns all admin accounts ordered by id.
func (s *AdminStore) List(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, login_id, pw_hash, name, COALESCE(dept,''), role, active, created_at FROM admin_user ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminUser
	for rows.Next() {
		var u AdminUser
		var active int
		if err := rows.Scan(&u.ID, &u.LoginID, &u.PWHash, &u.Name, &u.Dept, &u.Role, &active, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Active = active != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountActiveAdmins returns how many accounts with role='admin' and active=1 exist.
func (s *AdminStore) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM admin_user WHERE role = 'admin' AND active = 1`).Scan(&n)
	return n, err
}

// SetActive activates/deactivates an account. Deactivating the last active
// admin is refused (SPEC §7.3 "마지막 활성 admin 비활성화 금지").
func (s *AdminStore) SetActive(ctx context.Context, id int64, active bool) error {
	if !active {
		u, err := s.FindByID(ctx, id)
		if err != nil {
			return err
		}
		if u.Role == RoleAdmin && u.Active {
			n, err := s.CountActiveAdmins(ctx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return ErrLastActiveAdmin
			}
		}
	}
	v := 0
	if active {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE admin_user SET active = ? WHERE id = ?`, v, id)
	return err
}

// SetPassword hashes and stores a new password for an existing admin.
func (s *AdminStore) SetPassword(ctx context.Context, id int64, newPassword string) error {
	u, err := s.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if err := ValidatePasswordPolicy(newPassword, u.LoginID, ""); err != nil {
		return err
	}
	hash, err := s.h.Hash(newPassword)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE admin_user SET pw_hash = ? WHERE id = ?`, hash, id)
	return err
}

// UpdateRole changes an account's role, refusing to demote the last active admin.
func (s *AdminStore) UpdateRole(ctx context.Context, id int64, role string) error {
	if role != RoleAdmin && role != RoleOperator {
		return ErrInvalidRole
	}
	u, err := s.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Role == RoleAdmin && role == RoleOperator && u.Active {
		n, err := s.CountActiveAdmins(ctx)
		if err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastActiveAdmin
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE admin_user SET role = ? WHERE id = ?`, role, id)
	return err
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	// modernc.org/sqlite wraps sqlite3 error text; match on message since it
	// doesn't export a typed constraint-violation error in this driver.
	msg := err.Error()
	return contains(msg, "UNIQUE constraint failed")
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

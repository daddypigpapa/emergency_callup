// Package settings stores a small number of runtime-editable key/value
// settings in SQLite (the `setting` table) — currently just the VWorld tile
// key, so an admin can change it from the web UI without a server restart.
// Anything not overridden here falls back to its environment-variable
// default from internal/config.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// TileKeyName is the setting key used for the VWorld tile API key (SPEC
// §10.1's TILE_KEY, made admin-editable).
const TileKeyName = "tile_key"

var ErrNotSet = errors.New("settings: key not set")

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Get returns the stored value for key, or ErrNotSet if no admin override
// has ever been saved (callers should fall back to their env-var default).
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotSet
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// Set stores/overwrites a value.
func (s *Store) Set(ctx context.Context, key, value, actor string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO setting(key, value, updated_by, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		key, value, actor, now.UnixMilli())
	return err
}

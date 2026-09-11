package roster

import (
	"context"
	"database/sql"
	"errors"
)

// Preset kinds, matching the `preset.kind` CHECK constraint (SPEC §4).
const (
	PresetIncidentType = "incident_type"
	PresetMessage      = "message"
	PresetMission      = "mission"
)

var ErrPresetNotFound = errors.New("roster: preset not found")

// Preset mirrors the `preset` table.
type Preset struct {
	ID     int64
	Kind   string
	Text   string
	Sort   int
	Active bool
}

// PresetStore manages the `preset` table (SPEC §7.3: 발령종류·메시지·임무 문구).
type PresetStore struct{ db *sql.DB }

func NewPresetStore(db *sql.DB) *PresetStore { return &PresetStore{db: db} }

func (s *PresetStore) Create(ctx context.Context, kind, text string, sort int) (*Preset, error) {
	if kind != PresetIncidentType && kind != PresetMessage && kind != PresetMission {
		return nil, errors.New("roster: invalid preset kind")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO preset(kind, text, sort, active) VALUES (?, ?, ?, 1)`, kind, text, sort)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Preset{ID: id, Kind: kind, Text: text, Sort: sort, Active: true}, nil
}

func (s *PresetStore) List(ctx context.Context, kind string, activeOnly bool) ([]Preset, error) {
	q := `SELECT id, kind, text, sort, active FROM preset WHERE kind = ?`
	if activeOnly {
		q += ` AND active = 1`
	}
	q += ` ORDER BY sort, id`
	rows, err := s.db.QueryContext(ctx, q, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Preset
	for rows.Next() {
		var p Preset
		var active int
		if err := rows.Scan(&p.ID, &p.Kind, &p.Text, &p.Sort, &active); err != nil {
			return nil, err
		}
		p.Active = active != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PresetStore) Update(ctx context.Context, id int64, text string, sort int, active bool) error {
	v := 0
	if active {
		v = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE preset SET text=?, sort=?, active=? WHERE id=?`, text, sort, v, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrPresetNotFound
	}
	return nil
}

func (s *PresetStore) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM preset WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrPresetNotFound
	}
	return nil
}

// Pre-registered per-team plans (docs/SPEC_AREA_EDITOR.md §3.2, §4.4): the
// peacetime mission/area/rally-point/checkpoints an admin sets up ahead of
// time for each team, independent of any incident. BuildDraft's §5.4
// auto-fill and incident open/team-task-update snapshot from these — see
// docs/SPEC_AREA_EDITOR.md §3.5 and §4.5 (wired up in a later work package).
package incident

import (
	"context"
	"database/sql"
	"fmt"
)

// MaxCheckpoints is the per-team checkpoint limit (docs/SPEC_AREA_EDITOR.md
// §3.2/§4.4).
const MaxCheckpoints = 10

var ErrTooManyCheckpoints = fmt.Errorf("incident: a team may have at most %d checkpoints", MaxCheckpoints)

// Checkpoint mirrors one row of the `checkpoint` table. JSON tags matter
// here: the same struct is marshaled into team_task.checkpoints as the
// incident-open/team-task-update snapshot (docs/SPEC_AREA_EDITOR.md §3.5).
type Checkpoint struct {
	Seq     int     `json:"seq"`
	Name    string  `json:"name"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	Addr    string  `json:"addr,omitempty"`
	RadiusM int     `json:"r"`
}

// TeamPlan mirrors `team_plan` plus its checkpoints — the pre-registered
// mission/area/rally/checkpoints for one team.
type TeamPlan struct {
	TeamNo      int64
	Mission     string
	AreaID      int64    // 0 = none set
	RallyLat    *float64 // nil = automatic (area center, docs/SPEC_AREA_EDITOR.md §3.4)
	RallyLng    *float64
	RallyAddr   string
	Checkpoints []Checkpoint
	UpdatedBy   string
	UpdatedAt   int64
}

// TeamPlanInput is one team's plan as submitted for saving. Checkpoints'
// Seq fields are ignored on input; SaveBatch assigns 1..n in slice order.
type TeamPlanInput struct {
	No          int64
	Mission     string
	AreaID      int64 // 0 = clear
	RallyLat    *float64
	RallyLng    *float64
	RallyAddr   string
	Checkpoints []Checkpoint
}

// PlanStore manages the `team_plan` and `checkpoint` tables.
type PlanStore struct{ db *sql.DB }

func NewPlanStore(db *sql.DB) *PlanStore { return &PlanStore{db: db} }

// List returns all 10 teams' plans in team-number order, one entry per team
// even if it has no team_plan row yet (docs/SPEC_AREA_EDITOR.md §4.4:
// "계획이 없는 조도 10개 전부 돌려준다") — such a team comes back with
// Mission="" and AreaID=0.
func (s *PlanStore) List(ctx context.Context) ([]TeamPlan, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.no,
		       COALESCE(tp.mission, ''), COALESCE(tp.area_id, 0),
		       tp.rally_lat, tp.rally_lng, COALESCE(tp.rally_addr, ''),
		       COALESCE(tp.updated_by, ''), COALESCE(tp.updated_at, 0)
		FROM team t
		LEFT JOIN team_plan tp ON tp.team_no = t.no
		WHERE t.no BETWEEN 1 AND 10
		ORDER BY t.no`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plans []TeamPlan
	for rows.Next() {
		var p TeamPlan
		var rallyLat, rallyLng sql.NullFloat64
		if err := rows.Scan(&p.TeamNo, &p.Mission, &p.AreaID, &rallyLat, &rallyLng, &p.RallyAddr,
			&p.UpdatedBy, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if rallyLat.Valid {
			v := rallyLat.Float64
			p.RallyLat = &v
		}
		if rallyLng.Valid {
			v := rallyLng.Float64
			p.RallyLng = &v
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	cps, err := s.checkpointsByTeam(ctx)
	if err != nil {
		return nil, err
	}
	for i := range plans {
		plans[i].Checkpoints = cps[plans[i].TeamNo]
	}
	return plans, nil
}

func (s *PlanStore) checkpointsByTeam(ctx context.Context) (map[int64][]Checkpoint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT team_no, seq, name, lat, lng, COALESCE(addr,''), radius_m FROM checkpoint ORDER BY team_no, seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Checkpoint{}
	for rows.Next() {
		var teamNo int64
		var cp Checkpoint
		if err := rows.Scan(&teamNo, &cp.Seq, &cp.Name, &cp.Lat, &cp.Lng, &cp.Addr, &cp.RadiusM); err != nil {
			return nil, err
		}
		out[teamNo] = append(out[teamNo], cp)
	}
	return out, rows.Err()
}

// Get returns one team's plan, or a zero-value plan (Mission="", AreaID=0,
// no checkpoints) if it has none yet.
func (s *PlanStore) Get(ctx context.Context, teamNo int64) (*TeamPlan, error) {
	plans, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range plans {
		if plans[i].TeamNo == teamNo {
			return &plans[i], nil
		}
	}
	return &TeamPlan{TeamNo: teamNo}, nil
}

// SaveBatch upserts the given teams' plans and replaces each listed team's
// checkpoint list wholesale, all in one transaction. Teams not present in
// inputs are left untouched (docs/SPEC_AREA_EDITOR.md §4.4).
func (s *PlanStore) SaveBatch(ctx context.Context, inputs []TeamPlanInput, actor string, now int64) error {
	for _, in := range inputs {
		if len(in.Checkpoints) > MaxCheckpoints {
			return ErrTooManyCheckpoints
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, in := range inputs {
		var areaID any
		if in.AreaID != 0 {
			areaID = in.AreaID
		}
		var rallyLat, rallyLng any
		if in.RallyLat != nil {
			rallyLat = *in.RallyLat
		}
		if in.RallyLng != nil {
			rallyLng = *in.RallyLng
		}
		var rallyAddr any
		if in.RallyAddr != "" {
			rallyAddr = in.RallyAddr
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_plan(team_no, mission, area_id, rally_lat, rally_lng, rally_addr, updated_by, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(team_no) DO UPDATE SET
				mission = excluded.mission,
				area_id = excluded.area_id,
				rally_lat = excluded.rally_lat,
				rally_lng = excluded.rally_lng,
				rally_addr = excluded.rally_addr,
				updated_by = excluded.updated_by,
				updated_at = excluded.updated_at`,
			in.No, in.Mission, areaID, rallyLat, rallyLng, rallyAddr, actor, now); err != nil {
			return fmt.Errorf("incident: save team_plan for team %d: %w", in.No, err)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM checkpoint WHERE team_no = ?`, in.No); err != nil {
			return fmt.Errorf("incident: clear checkpoints for team %d: %w", in.No, err)
		}
		for i, cp := range in.Checkpoints {
			seq := i + 1
			var addr any
			if cp.Addr != "" {
				addr = cp.Addr
			}
			radiusM := cp.RadiusM
			if radiusM == 0 {
				radiusM = 50
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO checkpoint(team_no, seq, name, lat, lng, addr, radius_m) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				in.No, seq, cp.Name, cp.Lat, cp.Lng, addr, radiusM); err != nil {
				return fmt.Errorf("incident: insert checkpoint %d for team %d: %w", seq, in.No, err)
			}
		}
	}

	return tx.Commit()
}

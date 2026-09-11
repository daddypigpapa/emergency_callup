package incident

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"emergencycallup/internal/audit"
)

// Store manages incidents, team tasks and assignments.
type Store struct {
	db    *sql.DB
	audit *audit.Log
}

func NewStore(db *sql.DB, a *audit.Log) *Store { return &Store{db: db, audit: a} }

// GetActive returns the current active incident, or ErrNoActiveIncident.
func (s *Store) GetActive(ctx context.Context) (*Incident, error) {
	return s.queryOne(ctx, `SELECT id, type_text, message, status, version, opened_by, opened_at, COALESCE(closed_by,''), COALESCE(closed_at,0)
		FROM incident WHERE status = 'active'`)
}

// GetByID returns an incident regardless of status.
func (s *Store) GetByID(ctx context.Context, id int64) (*Incident, error) {
	return s.queryOne(ctx, `SELECT id, type_text, message, status, version, opened_by, opened_at, COALESCE(closed_by,''), COALESCE(closed_at,0)
		FROM incident WHERE id = ?`, id)
}

func (s *Store) queryOne(ctx context.Context, q string, args ...any) (*Incident, error) {
	row := s.db.QueryRowContext(ctx, q, args...)
	var inc Incident
	if err := row.Scan(&inc.ID, &inc.TypeText, &inc.Message, &inc.Status, &inc.Version, &inc.OpenedBy, &inc.OpenedAt, &inc.ClosedBy, &inc.ClosedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoActiveIncident
		}
		return nil, err
	}
	return &inc, nil
}

// activeMemberIDsForTeam returns active member IDs whose team_no matches,
// with their peacetime mission/area for override resolution.
type peacetimeMember struct {
	ID      int64
	Mission string
	AreaID  int64
}

// Open creates a new active incident with the given team tasks and member
// overrides (SPEC §7.3 POST /a/incidents). Every active member belonging to
// one of the requested teams gets an assignment row in state NOTIFIED.
func (s *Store) Open(ctx context.Context, typeText, message string, teams []TeamInput, overrides []MemberOverrideInput, actor string, now time.Time) (*Incident, error) {
	if len(teams) == 0 {
		return nil, ErrNoTeamsRequested
	}
	for _, t := range teams {
		if t.Mission == "" || t.AreaID == 0 {
			return nil, ErrTeamMissingValues
		}
	}
	overrideByMember := make(map[int64]MemberOverrideInput, len(overrides))
	for _, o := range overrides {
		overrideByMember[o.MemberID] = o
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	nowMs := now.UnixMilli()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO incident(type_text, message, status, version, opened_by, opened_at) VALUES (?, ?, 'active', 1, ?, ?)`,
		typeText, message, actor, nowMs)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, ErrIncidentActive
		}
		return nil, err
	}
	incidentID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	for _, t := range teams {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO team_task(incident_id, team_no, mission, area_id, version, updated_by, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?)`,
			incidentID, t.No, t.Mission, t.AreaID, actor, nowMs); err != nil {
			return nil, err
		}

		members, err := s.activeMembersForTeamTx(ctx, tx, t.No)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			effMission, effArea := t.Mission, t.AreaID
			var missionOverride any
			var areaOverride any
			if o, ok := overrideByMember[m.ID]; ok {
				if o.Mission != nil {
					effMission = *o.Mission
					missionOverride = *o.Mission
				}
				if o.AreaID != nil {
					effArea = *o.AreaID
					areaOverride = *o.AreaID
				}
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO assignment(incident_id, member_id, team_no, mission_override, area_override_id, eff_mission, eff_area_id, mission_ver, ack_ver, state, left_count, suspect)
				 VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?, 0, 0)`,
				incidentID, m.ID, t.No, missionOverride, areaOverride, effMission, effArea, StateNotified); err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = s.audit.Record(ctx, now, audit.Event{Type: "incident_open", Actor: actor, IncidentID: &incidentID,
		Data: map[string]any{"type": typeText}})
	return s.GetByID(ctx, incidentID)
}

func (s *Store) activeMembersForTeamTx(ctx context.Context, tx *sql.Tx, teamNo int64) ([]peacetimeMember, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, COALESCE(mission,''), COALESCE(area_id,0) FROM member WHERE active=1 AND team_no=?`, teamNo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []peacetimeMember
	for rows.Next() {
		var m peacetimeMember
		if err := rows.Scan(&m.ID, &m.Mission, &m.AreaID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for i := 0; i+len("UNIQUE constraint failed") <= len(msg); i++ {
		if msg[i:i+len("UNIQUE constraint failed")] == "UNIQUE constraint failed" {
			return true
		}
	}
	return false
}

// UpdateMeta changes type/message only (SPEC §5.3 rule 6: version bump, no
// assignment impact, not an ack-popup trigger).
func (s *Store) UpdateMeta(ctx context.Context, typeText, message *string, actor string, now time.Time) (*Incident, error) {
	inc, err := s.GetActive(ctx)
	if err != nil {
		return nil, err
	}
	nt, nm := inc.TypeText, inc.Message
	if typeText != nil {
		nt = *typeText
	}
	if message != nil {
		nm = *message
	}
	_, err = s.db.ExecContext(ctx, `UPDATE incident SET type_text=?, message=?, version=version+1 WHERE id=?`, nt, nm, inc.ID)
	if err != nil {
		return nil, err
	}
	_ = s.audit.Record(ctx, now, audit.Event{Type: "incident_update", Actor: actor, IncidentID: &inc.ID})
	return s.GetByID(ctx, inc.ID)
}

// GetTeamTask returns one team's task within an incident.
func (s *Store) GetTeamTask(ctx context.Context, incidentID, teamNo int64) (*TeamTask, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT incident_id, team_no, mission, area_id, version, updated_by, updated_at FROM team_task WHERE incident_id=? AND team_no=?`,
		incidentID, teamNo)
	var tt TeamTask
	if err := row.Scan(&tt.IncidentID, &tt.TeamNo, &tt.Mission, &tt.AreaID, &tt.Version, &tt.UpdatedBy, &tt.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTeamTaskNotFound
		}
		return nil, err
	}
	return &tt, nil
}

// ListTeamTasks returns every team task for an incident.
func (s *Store) ListTeamTasks(ctx context.Context, incidentID int64) ([]TeamTask, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT incident_id, team_no, mission, area_id, version, updated_by, updated_at FROM team_task WHERE incident_id=? ORDER BY team_no`,
		incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TeamTask
	for rows.Next() {
		var tt TeamTask
		if err := rows.Scan(&tt.IncidentID, &tt.TeamNo, &tt.Mission, &tt.AreaID, &tt.Version, &tt.UpdatedBy, &tt.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, tt)
	}
	return out, rows.Err()
}

// UpdateTeamTask changes a team's mission/area (SPEC §5.3, §7.3
// PUT /a/incidents/current/teams/{no}). Only assignments that (a) belong to
// this team, (b) don't carry a personal override for the changed field, and
// (c) actually end up with a different effective value get mission_ver
// bumped. Members with an area change revert ARRIVED/LEFT to MOVING
// (SPEC §5.3 rule 3). Other teams' rows are never touched (R2).
func (s *Store) UpdateTeamTask(ctx context.Context, incidentID, teamNo int64, mission string, areaID int64, actor string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE team_task SET mission=?, area_id=?, version=version+1, updated_by=?, updated_at=? WHERE incident_id=? AND team_no=?`,
		mission, areaID, actor, now.UnixMilli(), incidentID, teamNo); err != nil {
		return err
	}

	rows2, err := tx.QueryContext(ctx,
		`SELECT member_id, mission_override, area_override_id, eff_mission, eff_area_id, mission_ver, state
		 FROM assignment WHERE incident_id=? AND team_no=?`, incidentID, teamNo)
	if err != nil {
		return err
	}
	defer rows2.Close()
	for rows2.Next() {
		var memberID int64
		var missionOv sql.NullString
		var areaOv sql.NullInt64
		var effMission string
		var effArea int64
		var missionVer int
		var state string
		if err := rows2.Scan(&memberID, &missionOv, &areaOv, &effMission, &effArea, &missionVer, &state); err != nil {
			return err
		}
		newMission, newArea := effMission, effArea
		if !missionOv.Valid {
			newMission = mission
		}
		if !areaOv.Valid {
			newArea = areaID
		}
		if newMission == effMission && newArea == effArea {
			continue // no effective change for this member
		}
		newVer := missionVer
		if newMission != effMission || newArea != effArea {
			newVer++
		}
		newState := state
		var newArrivedAt any
		if newArea != effArea {
			if state == StateArrived || state == StateLeft {
				newState = StateMoving
				newArrivedAt = nil
			}
		}
		if newArea != effArea && (state == StateArrived || state == StateLeft) {
			if _, err := tx.ExecContext(ctx,
				`UPDATE assignment SET eff_mission=?, eff_area_id=?, mission_ver=?, state=?, arrived_at=? WHERE incident_id=? AND member_id=?`,
				newMission, newArea, newVer, newState, newArrivedAt, incidentID, memberID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx,
				`UPDATE assignment SET eff_mission=?, eff_area_id=?, mission_ver=? WHERE incident_id=? AND member_id=?`,
				newMission, newArea, newVer, incidentID, memberID); err != nil {
				return err
			}
		}
	}
	if err := rows2.Err(); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	_ = s.audit.Record(ctx, now, audit.Event{Type: "team_task_update", Actor: actor, IncidentID: &incidentID,
		Data: map[string]any{"teamNo": teamNo, "mission": mission, "areaId": areaID}})
	return nil
}

// UpdateMemberOverride sets/clears one member's personal mission/area
// override (SPEC §7.3 PUT /a/incidents/current/members/{id}). A nil pointer
// field means "follow the team task" for that field.
func (s *Store) UpdateMemberOverride(ctx context.Context, incidentID, memberID int64, mission *string, areaID *int64, actor string, now time.Time) error {
	tt, err := s.getAssignmentTeam(ctx, incidentID, memberID)
	if err != nil {
		return err
	}
	team, err := s.GetTeamTask(ctx, incidentID, tt)
	if err != nil {
		return err
	}

	a, err := s.GetAssignment(ctx, incidentID, memberID)
	if err != nil {
		return err
	}

	newMission := team.Mission
	var missionOv any
	if mission != nil {
		newMission = *mission
		missionOv = *mission
	}
	newArea := team.AreaID
	var areaOv any
	if areaID != nil {
		newArea = *areaID
		areaOv = *areaID
	}

	changed := newMission != a.EffMission || newArea != a.EffAreaID
	newVer := a.MissionVer
	if changed {
		newVer++
	}
	newState := a.State
	var newArrivedAt any
	if newArea != a.EffAreaID && (a.State == StateArrived || a.State == StateLeft) {
		newState = StateMoving
		newArrivedAt = nil
		if _, err := s.db.ExecContext(ctx,
			`UPDATE assignment SET mission_override=?, area_override_id=?, eff_mission=?, eff_area_id=?, mission_ver=?, state=?, arrived_at=? WHERE incident_id=? AND member_id=?`,
			missionOv, areaOv, newMission, newArea, newVer, newState, newArrivedAt, incidentID, memberID); err != nil {
			return err
		}
	} else {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE assignment SET mission_override=?, area_override_id=?, eff_mission=?, eff_area_id=?, mission_ver=? WHERE incident_id=? AND member_id=?`,
			missionOv, areaOv, newMission, newArea, newVer, incidentID, memberID); err != nil {
			return err
		}
	}
	_ = s.audit.Record(ctx, now, audit.Event{Type: "assignment_update", Actor: actor, IncidentID: &incidentID, MemberID: &memberID})
	return nil
}

func (s *Store) getAssignmentTeam(ctx context.Context, incidentID, memberID int64) (int64, error) {
	var teamNo int64
	err := s.db.QueryRowContext(ctx, `SELECT team_no FROM assignment WHERE incident_id=? AND member_id=?`, incidentID, memberID).Scan(&teamNo)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrAssignmentMissing
	}
	return teamNo, err
}

// AddMember creates a new NOTIFIED assignment for a member joining an
// already-open incident (SPEC §5.3 rule 7: existing rows untouched).
func (s *Store) AddMember(ctx context.Context, incidentID, memberID, teamNo int64, mission *string, areaID *int64, actor string, now time.Time) error {
	team, err := s.GetTeamTask(ctx, incidentID, teamNo)
	if err != nil {
		return err
	}
	effMission, effArea := team.Mission, team.AreaID
	var missionOv, areaOv any
	if mission != nil {
		effMission = *mission
		missionOv = *mission
	}
	if areaID != nil {
		effArea = *areaID
		areaOv = *areaID
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO assignment(incident_id, member_id, team_no, mission_override, area_override_id, eff_mission, eff_area_id, mission_ver, ack_ver, state, left_count, suspect)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?, 0, 0)`,
		incidentID, memberID, teamNo, missionOv, areaOv, effMission, effArea, StateNotified)
	if err != nil {
		return err
	}
	_ = s.audit.Record(ctx, now, audit.Event{Type: "assignment_add", Actor: actor, IncidentID: &incidentID, MemberID: &memberID})
	return nil
}

// GetAssignment returns one member's assignment within an incident.
func (s *Store) GetAssignment(ctx context.Context, incidentID, memberID int64) (*Assignment, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT incident_id, member_id, team_no, mission_override, area_override_id, eff_mission, eff_area_id,
		        mission_ver, ack_ver, state, first_login_at, arrived_at, last_left_at, left_count,
		        last_lat5, last_lng5, last_acc, last_fix_at, suspect
		 FROM assignment WHERE incident_id=? AND member_id=?`, incidentID, memberID)
	return scanAssignment(row)
}

// ListAssignments returns every assignment for an incident.
func (s *Store) ListAssignments(ctx context.Context, incidentID int64) ([]Assignment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT incident_id, member_id, team_no, mission_override, area_override_id, eff_mission, eff_area_id,
		        mission_ver, ack_ver, state, first_login_at, arrived_at, last_left_at, left_count,
		        last_lat5, last_lng5, last_acc, last_fix_at, suspect
		 FROM assignment WHERE incident_id=?`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func scanAssignment(row interface{ Scan(...any) error }) (*Assignment, error) {
	var a Assignment
	var missionOv sql.NullString
	var areaOvInt sql.NullInt64
	var firstLogin, arrivedAt, lastLeftAt, lastLat5, lastLng5, lastAcc, lastFixAt sql.NullInt64
	var suspect int
	if err := row.Scan(&a.IncidentID, &a.MemberID, &a.TeamNo, &missionOv, &areaOvInt, &a.EffMission, &a.EffAreaID,
		&a.MissionVer, &a.AckVer, &a.State, &firstLogin, &arrivedAt, &lastLeftAt, &a.LeftCount,
		&lastLat5, &lastLng5, &lastAcc, &lastFixAt, &suspect); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAssignmentMissing
		}
		return nil, err
	}
	if missionOv.Valid {
		v := missionOv.String
		a.MissionOverride = &v
	}
	if areaOvInt.Valid {
		v := areaOvInt.Int64
		a.AreaOverrideID = &v
	}
	if firstLogin.Valid {
		v := firstLogin.Int64
		a.FirstLoginAt = &v
	}
	if arrivedAt.Valid {
		v := arrivedAt.Int64
		a.ArrivedAt = &v
	}
	if lastLeftAt.Valid {
		v := lastLeftAt.Int64
		a.LastLeftAt = &v
	}
	if lastLat5.Valid {
		v := lastLat5.Int64
		a.LastLat5 = &v
	}
	if lastLng5.Valid {
		v := lastLng5.Int64
		a.LastLng5 = &v
	}
	if lastAcc.Valid {
		v := lastAcc.Int64
		a.LastAcc = &v
	}
	if lastFixAt.Valid {
		v := lastFixAt.Int64
		a.LastFixAt = &v
	}
	a.Suspect = suspect != 0
	return &a, nil
}

// Close ends the active incident (SPEC §5.2: one-way active -> closed).
// Session-expiry (§11.1 "사건 종료 1시간 후") is orchestrated by the caller
// via auth.SessionStore.ExpireMemberSessionsBy, since this package doesn't
// depend on auth.
func (s *Store) Close(ctx context.Context, actor string, now time.Time) (*Incident, error) {
	inc, err := s.GetActive(ctx)
	if err != nil {
		return nil, err
	}
	nowMs := now.UnixMilli()
	_, err = s.db.ExecContext(ctx, `UPDATE incident SET status='closed', closed_by=?, closed_at=? WHERE id=?`, actor, nowMs, inc.ID)
	if err != nil {
		return nil, err
	}
	_ = s.audit.Record(ctx, now, audit.Event{Type: "incident_close", Actor: actor, IncidentID: &inc.ID})
	return s.GetByID(ctx, inc.ID)
}

// DeleteOldFixes removes `fix` history rows older than cutoffMs, but only
// for incidents that are already closed (SPEC §11.4: "사건 종료 후
// FIX_RETENTION_DAYS 지나면 매일 새벽 삭제" — an active incident's history
// is never pruned no matter its age).
func (s *Store) DeleteOldFixes(ctx context.Context, cutoffMs int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM fix WHERE ts < ? AND incident_id IN (SELECT id FROM incident WHERE status = 'closed')`,
		cutoffMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LatestClosedIncidentForMember finds the most recently closed incident
// this member was assigned to, for the CLOSED-status window (SPEC §5.1,
// §6.3: "최근 배정 사건이 12시간 내 종료면").
func (s *Store) LatestClosedIncidentForMember(ctx context.Context, memberID int64) (*Incident, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT i.id, i.type_text, i.message, i.status, i.version, i.opened_by, i.opened_at, COALESCE(i.closed_by,''), COALESCE(i.closed_at,0)
		 FROM incident i JOIN assignment a ON a.incident_id = i.id
		 WHERE a.member_id = ? AND i.status = 'closed'
		 ORDER BY i.closed_at DESC LIMIT 1`, memberID)
	var inc Incident
	if err := row.Scan(&inc.ID, &inc.TypeText, &inc.Message, &inc.Status, &inc.Version, &inc.OpenedBy, &inc.OpenedAt, &inc.ClosedBy, &inc.ClosedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoActiveIncident
		}
		return nil, err
	}
	return &inc, nil
}

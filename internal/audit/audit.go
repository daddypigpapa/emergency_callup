// Package audit records the append-only `event` table (SPEC §4, §4.1).
// Rows are inserted, never updated or deleted (SPEC §4: "추가만, 수정·삭제 금지").
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Actor string prefixes, per SPEC §4 `event.actor` comment.
const (
	ActorSystem = "system"
)

// ActorAdmin formats the actor string for an admin-initiated event.
func ActorAdmin(loginID string) string { return "admin:" + loginID }

// ActorMember formats the actor string for a member-initiated event.
func ActorMember(memberID int64) string {
	return "member:" + formatInt(memberID)
}

func formatInt(v int64) string {
	// Avoid pulling in strconv at call sites repeatedly; trivial helper.
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Event is one row to append to the event table.
type Event struct {
	Type       string
	Actor      string
	IncidentID *int64 // nil when not tied to an incident
	MemberID   *int64 // nil when not about a specific member
	Data       any    // marshaled to JSON; may be nil
}

// Log appends events to the `event` table.
type Log struct {
	db *sql.DB
}

func New(db *sql.DB) *Log { return &Log{db: db} }

// Record inserts one event row. now is injected for testability.
func (l *Log) Record(ctx context.Context, now time.Time, e Event) error {
	var dataStr any
	if e.Data != nil {
		b, err := json.Marshal(e.Data)
		if err != nil {
			return err
		}
		dataStr = string(b)
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO event(ts, incident_id, actor, type, member_id, data) VALUES (?, ?, ?, ?, ?, ?)`,
		now.UnixMilli(), e.IncidentID, e.Actor, e.Type, e.MemberID, dataStr)
	return err
}

// Row is a read-back event row for GET /a/events.
type Row struct {
	ID         int64  `json:"id"`
	TS         int64  `json:"ts"`
	IncidentID *int64 `json:"incidentId,omitempty"`
	Actor      string `json:"actor"`
	Type       string `json:"type"`
	MemberID   *int64 `json:"memberId,omitempty"`
	Data       string `json:"data,omitempty"`
}

// ListByIncident returns events for one incident, newest first.
func (l *Log) ListByIncident(ctx context.Context, incidentID int64, limit int) ([]Row, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	rows, err := l.db.QueryContext(ctx,
		`SELECT id, ts, incident_id, actor, type, member_id, COALESCE(data,'')
		 FROM event WHERE incident_id = ? ORDER BY ts DESC, id DESC LIMIT ?`,
		incidentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.TS, &r.IncidentID, &r.Actor, &r.Type, &r.MemberID, &r.Data); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

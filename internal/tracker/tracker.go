package tracker

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"emergencycallup/internal/area"
	"emergencycallup/internal/audit"
	"emergencycallup/internal/incident"
)

// Tracker owns the in-memory live-tracking state for the active incident
// (SPEC §6.5: "활성 사건의 배정 500건을 기동·발령 시 메모리에 올린다") plus
// the 2-second batch writer and the admin delta sequence counter.
type Tracker struct {
	db        *sql.DB
	incidents *incident.Store
	areas     *area.Store
	audit     *audit.Log
	load      *LoadMonitor

	mu         sync.Mutex
	live       map[int64]*Live     // member_id -> Live
	areaCache  map[int64]*area.Row // eff_area_id -> Row, for the active incident only
	incidentID int64               // 0 = none active
	incVersion int

	seq   uint64 // atomic; monotonic within process lifetime
	epoch string // random per process start (SPEC §6.5)
}

func New(db *sql.DB, incidents *incident.Store, areas *area.Store, a *audit.Log) *Tracker {
	return &Tracker{
		db: db, incidents: incidents, areas: areas, audit: a,
		load:      NewLoadMonitor(),
		live:      map[int64]*Live{},
		areaCache: map[int64]*area.Row{},
		epoch:     newEpoch(),
	}
}

func newEpoch() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (t *Tracker) Epoch() string { return t.epoch }
func (t *Tracker) Seq() uint64   { return atomic.LoadUint64(&t.seq) }

func (t *Tracker) bumpSeq() uint64 { return atomic.AddUint64(&t.seq, 1) }

// LoadActive (re)loads live state from the DB for whatever incident is
// currently active. Call it at startup and after every admin mutation
// (incident open/close, team task update, member override, add member) so
// the in-memory state and the DB never drift.
//
// DECISION: for simplicity (SPEC §2.1's "단순함" bias, and given this
// system's small scale — a few team/area edits per incident, not per
// second), every reload marks all rows as changed-since for the admin
// delta feed rather than diffing precisely. Bandwidth cost is negligible at
// this scale (a few hundred rows, a few times per incident).
func (t *Tracker) LoadActive(ctx context.Context) error {
	inc, err := t.incidents.GetActive(ctx)
	if errors.Is(err, incident.ErrNoActiveIncident) {
		t.mu.Lock()
		t.live = map[int64]*Live{}
		t.areaCache = map[int64]*area.Row{}
		t.incidentID = 0
		t.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}

	assignments, err := t.incidents.ListAssignments(ctx, inc.ID)
	if err != nil {
		return err
	}

	areaIDs := map[int64]bool{}
	newLive := make(map[int64]*Live, len(assignments))
	for i := range assignments {
		a := &assignments[i]
		l := NewLive(a)
		newLive[a.MemberID] = l
		areaIDs[a.EffAreaID] = true
	}
	newAreaCache := make(map[int64]*area.Row, len(areaIDs))
	for id := range areaIDs {
		row, err := t.areas.GetByID(ctx, id)
		if err != nil {
			return err
		}
		newAreaCache[id] = row
	}

	t.mu.Lock()
	t.live = newLive
	t.areaCache = newAreaCache
	t.incidentID = inc.ID
	t.incVersion = inc.Version
	seq := t.bumpSeq()
	for _, l := range t.live {
		l.seqAt = seq
	}
	t.mu.Unlock()
	return nil
}

// FixResult is what POST /f/fix and GET /f/sync respond with (minus the
// enclosing incident/task JSON, which the httpapi layer adds).
type FixResult struct {
	Status string // assignment state, or IDLE/NONE/CLOSED
	IV     int    // incident.version
	MV     int    // mission_ver
	D      float64
	N      int
	Stop   bool
}

var ErrClockRewind = errors.New("tracker: internal clock error")

// ProcessFix implements SPEC §6.3's POST /fix pipeline for one member.
func (t *Tracker) ProcessFix(ctx context.Context, memberID int64, f Fix, now time.Time) (*FixResult, error) {
	t.mu.Lock()
	live, ok := t.live[memberID]
	if !ok {
		incidentID := t.incidentID
		t.mu.Unlock()
		return t.notAssignedResult(ctx, memberID, incidentID, now)
	}

	wasNotified := live.State == incident.StateNotified
	live.MarkLoggedIn(now.UnixMilli())

	ar := t.areaCache[live.EffAreaID]
	prevLat5, prevLng5, hadFix := live.LastLat5, live.LastLng5, live.HasLastFix

	transitioned, events := live.ApplyFix(f, ar, now.UnixMilli())

	sd := ar.SignedDistance(area.Point{Lat: f.Lat, Lng: f.Lng})
	overloaded := t.load.Overloaded(now)
	n := NextInterval(live.State, sd, ar.REq(), overloaded)
	incVersion := t.incVersion
	result := &FixResult{Status: live.State, IV: incVersion, MV: live.MissionVer, D: round1(sd), N: n}

	needSync := transitioned || wasNotified
	movedFar := false
	if !needSync && hadFix {
		movedFar = haversine5(prevLat5, prevLng5, live.LastLat5, live.LastLng5) >= 10
	}

	var seq uint64
	if needSync || movedFar || !hadFix {
		seq = t.bumpSeq()
		live.seqAt = seq
	}
	incidentID := t.incidentID
	t.mu.Unlock()

	if needSync {
		if err := t.syncFlush(ctx, incidentID, live, now, events); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// MarkLoggedIn implements SPEC §5.1's NOTIFIED -> LOGGED_IN transition on
// the first authenticated request of an incident (R17), for callers that
// only touch GET /f/me or GET /f/sync without ever posting a fix.
func (t *Tracker) MarkLoggedIn(ctx context.Context, memberID int64, now time.Time) error {
	t.mu.Lock()
	live, ok := t.live[memberID]
	if !ok {
		t.mu.Unlock()
		return nil
	}
	live.MarkLoggedIn(now.UnixMilli())
	seq := t.bumpSeq()
	live.seqAt = seq
	snap := *live
	incidentID := t.incidentID
	t.mu.Unlock()

	_, err := t.db.ExecContext(ctx, `UPDATE assignment SET state=?, first_login_at=? WHERE incident_id=? AND member_id=?`,
		snap.State, snap.FirstLoginAt, incidentID, memberID)
	return err
}

// Sync implements GET /f/sync: report current status/versions/interval
// without submitting a new location (SPEC §7.2). It never mutates
// judgement state.
func (t *Tracker) Sync(ctx context.Context, memberID int64, now time.Time) (*FixResult, error) {
	t.mu.Lock()
	live, ok := t.live[memberID]
	if !ok {
		incidentID := t.incidentID
		t.mu.Unlock()
		return t.notAssignedResult(ctx, memberID, incidentID, now)
	}
	ar := t.areaCache[live.EffAreaID]
	var sd float64
	if live.HasLastFix {
		sd = ar.SignedDistance(area.Point{Lat: float64(live.LastLat5) / 1e5, Lng: float64(live.LastLng5) / 1e5})
	} else {
		sd = ar.REq()*10 + 1000 // no fix yet: treat as far away for interval purposes
	}
	overloaded := t.load.Overloaded(now)
	n := NextInterval(live.State, sd, ar.REq(), overloaded)
	result := &FixResult{Status: live.State, IV: t.incVersion, MV: live.MissionVer, D: round1(sd), N: n}
	t.mu.Unlock()
	return result, nil
}

// notAssignedResult implements the IDLE/NONE/CLOSED branch of SPEC §6.3 step 2.
func (t *Tracker) notAssignedResult(ctx context.Context, memberID, activeIncidentID int64, now time.Time) (*FixResult, error) {
	if activeIncidentID != 0 {
		return &FixResult{Status: incident.StatusNone, N: 60}, nil
	}
	closedInc, err := t.incidents.LatestClosedIncidentForMember(ctx, memberID)
	if err == nil && now.UnixMilli()-closedInc.ClosedAt <= 12*time.Hour.Milliseconds() {
		return &FixResult{Status: incident.StatusClosed, Stop: true}, nil
	}
	return &FixResult{Status: incident.StatusIdle, N: 60}, nil
}

func round1(v float64) float64 {
	return float64(int64(v*10+sign(v)*0.5)) / 10
}
func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// haversine5 computes the distance in meters between two lat5/lng5
// (degrees * 1e5) integer points — a fast planar approximation adequate for
// the "moved >= 10m" seq-bump check (SPEC §6.3 step 5).
func haversine5(lat5a, lng5a, lat5b, lng5b int64) float64 {
	return area.Haversine(
		area.Point{Lat: float64(lat5a) / 1e5, Lng: float64(lng5a) / 1e5},
		area.Point{Lat: float64(lat5b) / 1e5, Lng: float64(lng5b) / 1e5},
	)
}

// syncFlush writes one member's transition (or first-login) synchronously,
// per SPEC §6.3 step 5, and records any judgement events.
func (t *Tracker) syncFlush(ctx context.Context, incidentID int64, l *Live, now time.Time, events []string) error {
	t.mu.Lock()
	snap := *l // copy under lock to avoid racing the field reads below
	t.mu.Unlock()

	var arrivedAt, lastLeftAt, firstLoginAt any
	if snap.ArrivedAt != 0 {
		arrivedAt = snap.ArrivedAt
	}
	if snap.LastLeftAt != 0 {
		lastLeftAt = snap.LastLeftAt
	}
	if snap.FirstLoginAt != 0 {
		firstLoginAt = snap.FirstLoginAt
	}
	suspect := 0
	if snap.Suspect {
		suspect = 1
	}
	_, err := t.db.ExecContext(ctx,
		`UPDATE assignment SET state=?, first_login_at=?, arrived_at=?, last_left_at=?, left_count=?,
		        last_lat5=?, last_lng5=?, last_acc=?, last_fix_at=?, suspect=?
		 WHERE incident_id=? AND member_id=?`,
		snap.State, firstLoginAt, arrivedAt, lastLeftAt, snap.LeftCount,
		snap.LastLat5, snap.LastLng5, snap.LastAcc, snap.LastFixAt, suspect,
		incidentID, snap.MemberID)
	if err != nil {
		return err
	}
	if _, err := t.db.ExecContext(ctx,
		`INSERT INTO fix(incident_id, member_id, ts, lat5, lng5, acc) VALUES (?, ?, ?, ?, ?, ?)`,
		incidentID, snap.MemberID, snap.LastFixAt, snap.LastLat5, snap.LastLng5, snap.LastAcc); err != nil {
		return err
	}
	for _, ev := range events {
		_ = t.audit.Record(ctx, now, audit.Event{Type: ev, Actor: audit.ActorMember(snap.MemberID), IncidentID: &incidentID, MemberID: &snap.MemberID})
	}
	t.mu.Lock()
	l.Dirty = false
	t.mu.Unlock()
	return nil
}

// FlushDirty is the periodic (2s) batch writer for non-transition position
// updates (SPEC §6.5). It should be called from a ticker goroutine started
// by cmd/server.
func (t *Tracker) FlushDirty(ctx context.Context) error {
	t.mu.Lock()
	type dirtyRow struct {
		memberID               int64
		lat5, lng5, acc, fixAt int64
		suspect                bool
	}
	var rows []dirtyRow
	incidentID := t.incidentID
	for _, l := range t.live {
		if l.Dirty {
			rows = append(rows, dirtyRow{l.MemberID, l.LastLat5, l.LastLng5, l.LastAcc, l.LastFixAt, l.Suspect})
			l.Dirty = false
		}
	}
	t.mu.Unlock()

	if len(rows) == 0 || incidentID == 0 {
		return nil
	}

	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		suspect := 0
		if r.suspect {
			suspect = 1
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE assignment SET last_lat5=?, last_lng5=?, last_acc=?, last_fix_at=?, suspect=? WHERE incident_id=? AND member_id=?`,
			r.lat5, r.lng5, r.acc, r.fixAt, suspect, incidentID, r.memberID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LiveSnapshot is a read-only view of one member's tracking state, used by
// the httpapi layer to build /a/snapshot and /a/delta responses.
type LiveSnapshot struct {
	MemberID   int64
	TeamNo     int64
	State      string
	LastLat5   int64
	LastLng5   int64
	LastAcc    int64
	LastFixAt  int64
	HasLastFix bool
	AckPending bool
	Suspect    bool
}

func toSnapshot(l *Live) LiveSnapshot {
	return LiveSnapshot{
		MemberID: l.MemberID, TeamNo: l.TeamNo, State: l.State,
		LastLat5: l.LastLat5, LastLng5: l.LastLng5, LastAcc: l.LastAcc, LastFixAt: l.LastFixAt,
		HasLastFix: l.HasLastFix, AckPending: l.MissionVer > l.AckVer, Suspect: l.Suspect,
	}
}

// SnapshotAll returns every tracked member (for GET /a/snapshot).
func (t *Tracker) SnapshotAll() []LiveSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]LiveSnapshot, 0, len(t.live))
	for _, l := range t.live {
		out = append(out, toSnapshot(l))
	}
	return out
}

// SnapshotSince returns members whose tracked state changed after seq
// `since` (for GET /a/delta).
func (t *Tracker) SnapshotSince(since uint64) []LiveSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []LiveSnapshot
	for _, l := range t.live {
		if l.seqAt > since {
			out = append(out, toSnapshot(l))
		}
	}
	return out
}

// IncidentID returns the currently tracked incident, or 0 if none.
func (t *Tracker) IncidentID() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.incidentID
}

// Ack implements POST /f/ack: records that the member has fetched the
// latest mission via GET /f/me (SPEC §7.2).
func (t *Tracker) Ack(ctx context.Context, memberID int64, mv int, now time.Time) error {
	t.mu.Lock()
	live, ok := t.live[memberID]
	if ok {
		if mv > live.AckVer {
			live.AckVer = mv
		}
		live.Dirty = true
	}
	t.mu.Unlock()
	if !ok {
		return nil
	}
	_, err := t.db.ExecContext(ctx, `UPDATE assignment SET ack_ver=? WHERE incident_id=? AND member_id=?`, mv, t.incidentID, memberID)
	return err
}

// ConsentDenied implements POST /f/consent {granted:false}.
func (t *Tracker) ConsentDenied(ctx context.Context, memberID int64, now time.Time) error {
	t.mu.Lock()
	live, ok := t.live[memberID]
	if !ok {
		t.mu.Unlock()
		return nil
	}
	live.MarkLocDenied()
	snap := *live
	t.mu.Unlock()
	_, err := t.db.ExecContext(ctx, `UPDATE assignment SET state=? WHERE incident_id=? AND member_id=?`, snap.State, t.incidentID, memberID)
	return err
}

// GetLive returns one member's live state (for GET /f/me, /f/sync).
func (t *Tracker) GetLive(memberID int64) (LiveSnapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	l, ok := t.live[memberID]
	if !ok {
		return LiveSnapshot{}, false
	}
	return toSnapshot(l), true
}

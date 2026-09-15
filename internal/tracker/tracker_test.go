package tracker

import (
	"context"
	"testing"
	"time"

	"emergencycallup/internal/area"
	"emergencycallup/internal/audit"
	"emergencycallup/internal/auth"
	"emergencycallup/internal/incident"
	"emergencycallup/internal/roster"
	"emergencycallup/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenMemory(t.Name())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

type harness struct {
	tracker  *Tracker
	incident *incident.Store
	members  *roster.Store
	areas    *area.Store
	db       *store.DB
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := testDB(t)
	al := audit.New(db.DB)
	areas := area.NewStore(db.DB)
	members := roster.NewStore(db.DB, auth.NewHasher())
	incidents := incident.NewStore(db.DB, al, areas, incident.NewPlanStore(db.DB))
	tr := New(db.DB, incidents, areas, al)
	return &harness{tracker: tr, incident: incidents, members: members, areas: areas, db: db}
}

// R1: no active incident -> no state/location stored, response is IDLE.
func TestProcessFix_NoActiveIncident_IsIdle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.tracker.LoadActive(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}
	res, err := h.tracker.ProcessFix(ctx, 999, Fix{Lat: 35.87, Lng: 128.60, Acc: 10, TS: time.Now().UnixMilli()}, time.Now())
	if err != nil {
		t.Fatalf("process fix: %v", err)
	}
	if res.Status != incident.StatusIdle {
		t.Errorf("status = %s, want IDLE", res.Status)
	}
}

// R1: active incident but member not assigned -> NONE, no storage.
func TestProcessFix_ActiveIncidentNotAssigned_IsNone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a1 := mustArea(t, h.areas, "지역A", 35.87, 128.60)
	mustMember(t, h.members, 1, "", 0, "00000001")
	if _, err := h.incident.Open(ctx, "T", "M", []incident.TeamInput{{No: 1, Mission: "m", AreaID: a1}}, nil, "admin:a", time.Now()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := h.tracker.LoadActive(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}
	res, err := h.tracker.ProcessFix(ctx, 99999, Fix{Lat: 35.87, Lng: 128.60, Acc: 10, TS: time.Now().UnixMilli()}, time.Now())
	if err != nil {
		t.Fatalf("process fix: %v", err)
	}
	if res.Status != incident.StatusNone {
		t.Errorf("status = %s, want NONE", res.Status)
	}
}

func TestProcessFix_ArrivalPersistsToDB(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a1 := mustArea(t, h.areas, "지역A", 35.8714, 128.6014)
	mID := mustMember(t, h.members, 1, "", 0, "00000001")
	got, err := h.incident.Open(ctx, "T", "M", []incident.TeamInput{{No: 1, Mission: "m", AreaID: a1}}, nil, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := h.tracker.LoadActive(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}

	now := time.Now()
	baseMs := now.UnixMilli()
	fix1 := Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: baseMs}
	if _, err := h.tracker.ProcessFix(ctx, mID, fix1, now); err != nil {
		t.Fatalf("fix1: %v", err)
	}
	fix2 := Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: baseMs + 11000}
	res, err := h.tracker.ProcessFix(ctx, mID, fix2, now.Add(11*time.Second))
	if err != nil {
		t.Fatalf("fix2: %v", err)
	}
	if res.Status != incident.StateArrived {
		t.Fatalf("status = %s, want ARRIVED", res.Status)
	}

	asg, err := h.incident.GetAssignment(ctx, got.ID, mID)
	if err != nil {
		t.Fatalf("get assignment: %v", err)
	}
	if asg.State != incident.StateArrived {
		t.Errorf("DB state = %s, want ARRIVED (sync flush should have persisted immediately)", asg.State)
	}
	if asg.ArrivedAt == nil || *asg.ArrivedAt != baseMs {
		t.Errorf("arrived_at = %v, want %d", asg.ArrivedAt, baseMs)
	}
}

// R3 / H1: after close, tracker no longer tracks the member, response is CLOSED.
func TestProcessFix_AfterClose_ReturnsClosedAndStopsStoring(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a1 := mustArea(t, h.areas, "지역A", 35.8714, 128.6014)
	mID := mustMember(t, h.members, 1, "", 0, "00000001")
	if _, err := h.incident.Open(ctx, "T", "M", []incident.TeamInput{{No: 1, Mission: "m", AreaID: a1}}, nil, "admin:a", time.Now()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := h.tracker.LoadActive(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := h.incident.Close(ctx, "admin:a", time.Now()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := h.tracker.LoadActive(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}

	res, err := h.tracker.ProcessFix(ctx, mID, Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: time.Now().UnixMilli()}, time.Now())
	if err != nil {
		t.Fatalf("process fix: %v", err)
	}
	if res.Status != incident.StatusClosed || !res.Stop {
		t.Errorf("expected CLOSED+stop, got %+v", res)
	}
}

func TestFlushDirty_WritesNonTransitionUpdates(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a1 := mustArea(t, h.areas, "지역A", 35.8714, 128.6014)
	mID := mustMember(t, h.members, 1, "", 0, "00000001")
	got, err := h.incident.Open(ctx, "T", "M", []incident.TeamInput{{No: 1, Mission: "m", AreaID: a1}}, nil, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := h.tracker.LoadActive(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}

	// A far-away point, well outside the area: MOVING -> MOVING, no transition,
	// but the position must still eventually land in the DB via FlushDirty.
	now := time.Now()
	if _, err := h.tracker.ProcessFix(ctx, mID, Fix{Lat: 35.95, Lng: 128.62, Acc: 10, TS: now.UnixMilli()}, now); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if err := h.tracker.FlushDirty(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	asg, err := h.incident.GetAssignment(ctx, got.ID, mID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if asg.LastLat5 == nil || *asg.LastLat5 != int64(3595000) {
		t.Errorf("last_lat5 = %v, want 3595000", asg.LastLat5)
	}
}

func TestAck_UpdatesAckVer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a1 := mustArea(t, h.areas, "지역A", 35.87, 128.60)
	mID := mustMember(t, h.members, 1, "", 0, "00000001")
	got, err := h.incident.Open(ctx, "T", "M", []incident.TeamInput{{No: 1, Mission: "m", AreaID: a1}}, nil, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := h.incident.UpdateTeamTask(ctx, got.ID, 1, "새임무", a1, "admin:a", time.Now()); err != nil {
		t.Fatalf("update team task: %v", err)
	}
	if err := h.tracker.LoadActive(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := h.tracker.Ack(ctx, mID, 2, time.Now()); err != nil {
		t.Fatalf("ack: %v", err)
	}
	asg, err := h.incident.GetAssignment(ctx, got.ID, mID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if asg.AckVer != 2 {
		t.Errorf("ack_ver = %d, want 2", asg.AckVer)
	}
}

func mustArea(t *testing.T, s *area.Store, name string, lat, lng float64) int64 {
	t.Helper()
	row, err := s.CreateCircle(context.Background(), name, area.Point{Lat: lat, Lng: lng}, 100, nil, time.Now())
	if err != nil {
		t.Fatalf("create area %s: %v", name, err)
	}
	return row.ID
}

func mustMember(t *testing.T, s *roster.Store, teamNo int64, mission string, areaID int64, mobileSuffix string) int64 {
	t.Helper()
	m, _, err := s.Create(context.Background(), roster.NewInput{
		LoginID: "u" + mobileSuffix, Dept: "dept", Name: "n" + mobileSuffix,
		Mobile: "010" + mobileSuffix, TeamNo: teamNo, Mission: mission, AreaID: areaID,
	}, time.Now())
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	return m.ID
}

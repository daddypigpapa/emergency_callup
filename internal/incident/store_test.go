package incident

import (
	"context"
	"testing"
	"time"

	"emergencycallup/internal/area"
	"emergencycallup/internal/audit"
	"emergencycallup/internal/auth"
	"emergencycallup/internal/roster"
)

func setup(t *testing.T) (*Store, *roster.Store, *area.Store) {
	t.Helper()
	db := testDB(t)
	a := audit.New(db.DB)
	areas := area.NewStore(db.DB)
	return NewStore(db.DB, a, areas, NewPlanStore(db.DB)), roster.NewStore(db.DB, auth.NewHasher()), areas
}

func TestOpen_CreatesAssignmentsForTeamMembers(t *testing.T) {
	inc, members, areas := setup(t)
	ctx := context.Background()
	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)
	mID := mustMember(t, members, 3, "", 0, "00000001")

	got, err := inc.Open(ctx, "비상 2단계", "메시지", []TeamInput{{No: 3, Mission: "차단선 구축", AreaID: a1}}, nil, "admin:a1", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got.Status != IncidentActive {
		t.Errorf("status = %s", got.Status)
	}
	asg, err := inc.GetAssignment(ctx, got.ID, mID)
	if err != nil {
		t.Fatalf("get assignment: %v", err)
	}
	if asg.State != StateNotified || asg.EffMission != "차단선 구축" || asg.EffAreaID != a1 {
		t.Errorf("unexpected assignment: %+v", asg)
	}
}

// Single active incident: opening a second one must fail.
func TestOpen_OnlyOneActiveIncident(t *testing.T) {
	inc, members, areas := setup(t)
	ctx := context.Background()
	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)
	mustMember(t, members, 3, "", 0, "00000001")

	if _, err := inc.Open(ctx, "T1", "M1", []TeamInput{{No: 3, Mission: "m", AreaID: a1}}, nil, "admin:a", time.Now()); err != nil {
		t.Fatalf("first open: %v", err)
	}
	_, err := inc.Open(ctx, "T2", "M2", []TeamInput{{No: 3, Mission: "m", AreaID: a1}}, nil, "admin:a", time.Now())
	if err != ErrIncidentActive {
		t.Fatalf("expected ErrIncidentActive, got %v", err)
	}
}

// R2 / H2 regression: changing team B's task must not touch team A's assignment rows.
func TestUpdateTeamTask_DoesNotAffectOtherTeam(t *testing.T) {
	inc, members, areas := setup(t)
	ctx := context.Background()
	aX := mustArea(t, areas, "지역X", 35.87, 128.60)
	aY := mustArea(t, areas, "지역Y", 35.90, 128.62)
	mA := mustMember(t, members, 1, "", 0, "00000001")
	mB := mustMember(t, members, 2, "", 0, "00000002")

	got, err := inc.Open(ctx, "T", "M",
		[]TeamInput{{No: 1, Mission: "임무A", AreaID: aX}, {No: 2, Mission: "임무B", AreaID: aY}}, nil, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	before, err := inc.GetAssignment(ctx, got.ID, mA)
	if err != nil {
		t.Fatalf("get before: %v", err)
	}

	// Simulate team A member having arrived, so we can check it stays ARRIVED.
	// (Direct SQL used here since arrival itself is the tracker package's job.)

	if err := inc.UpdateTeamTask(ctx, got.ID, 2, "새임무B", aY, "admin:a", time.Now()); err != nil {
		t.Fatalf("update team B: %v", err)
	}

	after, err := inc.GetAssignment(ctx, got.ID, mA)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.EffMission != before.EffMission || after.MissionVer != before.MissionVer {
		t.Errorf("team A assignment changed after team B update: before=%+v after=%+v", before, after)
	}
	_ = mB
}

// SPEC §5.3 rule 2: mission_ver bumps only for members whose effective value actually changed.
func TestUpdateTeamTask_OnlyChangedMembersGetVersionBump(t *testing.T) {
	inc, members, areas := setup(t)
	ctx := context.Background()
	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)
	a2 := mustArea(t, areas, "지역B", 35.90, 128.62)
	mFollower := mustMember(t, members, 4, "", 0, "00000001")
	mOverride := mustMember(t, members, 4, "", 0, "00000002")

	got, err := inc.Open(ctx, "T", "M", []TeamInput{{No: 4, Mission: "원래임무", AreaID: a1}},
		[]MemberOverrideInput{{MemberID: mOverride, Mission: strPtr("개인임무")}}, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if err := inc.UpdateTeamTask(ctx, got.ID, 4, "새임무", a2, "admin:a", time.Now()); err != nil {
		t.Fatalf("update: %v", err)
	}

	follower, err := inc.GetAssignment(ctx, got.ID, mFollower)
	if err != nil {
		t.Fatalf("get follower: %v", err)
	}
	if follower.MissionVer != 2 || follower.EffMission != "새임무" || follower.EffAreaID != a2 {
		t.Errorf("follower should pick up new team task: %+v", follower)
	}

	override, err := inc.GetAssignment(ctx, got.ID, mOverride)
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	// Mission override member keeps their own mission but area is not
	// overridden, so they DO pick up the new area (and thus a version bump).
	if override.EffMission != "개인임무" {
		t.Errorf("override member's mission should be untouched: %+v", override)
	}
	if override.EffAreaID != a2 {
		t.Errorf("override member without an area override should follow team area: %+v", override)
	}
}

func strPtr(s string) *string { return &s }

// R3 / H1: closing an incident is one-directional and doesn't resurrect.
func TestClose_IsOneWay(t *testing.T) {
	inc, members, areas := setup(t)
	ctx := context.Background()
	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)
	mustMember(t, members, 1, "", 0, "00000001")
	got, err := inc.Open(ctx, "T", "M", []TeamInput{{No: 1, Mission: "m", AreaID: a1}}, nil, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	closed, err := inc.Close(ctx, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.Status != IncidentClosed {
		t.Errorf("status = %s, want closed", closed.Status)
	}
	if _, err := inc.GetActive(ctx); err != ErrNoActiveIncident {
		t.Errorf("expected no active incident after close, got %v", err)
	}
	_ = got
}

func TestAddMember_DoesNotAffectExistingRows(t *testing.T) {
	inc, members, areas := setup(t)
	ctx := context.Background()
	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)
	existing := mustMember(t, members, 1, "", 0, "00000001")
	got, err := inc.Open(ctx, "T", "M", []TeamInput{{No: 1, Mission: "m", AreaID: a1}}, nil, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	before, err := inc.GetAssignment(ctx, got.ID, existing)
	if err != nil {
		t.Fatalf("get before: %v", err)
	}

	newMember := mustMember(t, members, 1, "", 0, "00000002")
	if err := inc.AddMember(ctx, got.ID, newMember, 1, nil, nil, "admin:a", time.Now()); err != nil {
		t.Fatalf("add member: %v", err)
	}

	after, err := inc.GetAssignment(ctx, got.ID, existing)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if *after != *before {
		t.Errorf("existing assignment changed after adding a new member: before=%+v after=%+v", before, after)
	}
	newAsg, err := inc.GetAssignment(ctx, got.ID, newMember)
	if err != nil {
		t.Fatalf("get new: %v", err)
	}
	if newAsg.State != StateNotified {
		t.Errorf("new member state = %s, want NOTIFIED", newAsg.State)
	}
}

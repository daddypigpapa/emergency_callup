package incident

import (
	"context"
	"testing"
	"time"

	"emergencycallup/internal/area"
	"emergencycallup/internal/auth"
	"emergencycallup/internal/roster"
)

// R25: opening an incident snapshots the team's plan rally/checkpoints into
// team_task when the incident uses the SAME area as the plan; a DIFFERENT
// area falls back to that area's own nav with no checkpoints.
func TestOpen_SnapshotsTeamPlanWhenAreaMatches(t *testing.T) {
	inc, members, areas := setup(t)
	ctx := context.Background()
	planned := mustArea(t, areas, "계획지역", 35.87, 128.60)
	other := mustArea(t, areas, "다른지역", 35.90, 128.63)
	mustMember(t, members, 1, "", 0, "00000001")

	plans := NewPlanStore(inc.db)
	rallyLat, rallyLng := 35.871, 128.601
	if err := plans.SaveBatch(ctx, []TeamPlanInput{
		{No: 1, Mission: "계획임무", AreaID: planned, RallyLat: &rallyLat, RallyLng: &rallyLng,
			Checkpoints: []Checkpoint{{Name: "cp1", Lat: 35.868, Lng: 128.598}}},
	}, "admin1", 1000); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	// Same area as the plan: rally + checkpoints come from the plan.
	got, err := inc.Open(ctx, "유형", "메시지", []TeamInput{{No: 1, Mission: "발령임무", AreaID: planned}}, nil, "admin1", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tt, err := inc.GetTeamTask(ctx, got.ID, 1)
	if err != nil {
		t.Fatalf("get team task: %v", err)
	}
	if tt.RallyLat != rallyLat || tt.RallyLng != rallyLng {
		t.Errorf("rally = (%f,%f), want plan's (%f,%f)", tt.RallyLat, tt.RallyLng, rallyLat, rallyLng)
	}
	if len(tt.Checkpoints) != 1 || tt.Checkpoints[0].Name != "cp1" {
		t.Errorf("checkpoints = %+v, want 1 from plan", tt.Checkpoints)
	}

	if _, err := inc.Close(ctx, "admin1", time.Now()); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Different area than the plan: falls back to that area's own nav, no checkpoints.
	got2, err := inc.Open(ctx, "유형2", "메시지2", []TeamInput{{No: 1, Mission: "발령임무2", AreaID: other}}, nil, "admin1", time.Now())
	if err != nil {
		t.Fatalf("open (different area): %v", err)
	}
	tt2, err := inc.GetTeamTask(ctx, got2.ID, 1)
	if err != nil {
		t.Fatalf("get team task 2: %v", err)
	}
	otherArea, err := areas.GetByID(ctx, other)
	if err != nil {
		t.Fatalf("get area: %v", err)
	}
	if tt2.RallyLat != otherArea.NavLat || tt2.RallyLng != otherArea.NavLng {
		t.Errorf("rally = (%f,%f), want area's own nav (%f,%f)", tt2.RallyLat, tt2.RallyLng, otherArea.NavLat, otherArea.NavLng)
	}
	if len(tt2.Checkpoints) != 0 {
		t.Errorf("checkpoints = %+v, want none (different area than plan)", tt2.Checkpoints)
	}
}

// R25 (auto rally): a plan with no explicit rally point (RallyLat/Lng nil)
// still snapshots to the area's nav, same as having no plan at all.
func TestOpen_SnapshotsAreaNavWhenPlanRallyIsAutomatic(t *testing.T) {
	inc, members, areas := setup(t)
	ctx := context.Background()
	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)
	mustMember(t, members, 2, "", 0, "00000002")

	plans := NewPlanStore(inc.db)
	if err := plans.SaveBatch(ctx, []TeamPlanInput{{No: 2, Mission: "임무", AreaID: a1}}, "admin1", 1000); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	got, err := inc.Open(ctx, "유형", "메시지", []TeamInput{{No: 2, Mission: "발령임무", AreaID: a1}}, nil, "admin1", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tt, err := inc.GetTeamTask(ctx, got.ID, 2)
	if err != nil {
		t.Fatalf("get team task: %v", err)
	}
	ar, err := areas.GetByID(ctx, a1)
	if err != nil {
		t.Fatalf("get area: %v", err)
	}
	if tt.RallyLat != ar.NavLat || tt.RallyLng != ar.NavLng {
		t.Errorf("rally = (%f,%f), want area nav (%f,%f)", tt.RallyLat, tt.RallyLng, ar.NavLat, ar.NavLng)
	}
}

// R26: editing a team's plan after an incident opened must not change the
// already-snapshotted team_task.
func TestOpen_LaterPlanEditsDoNotAffectOpenIncident(t *testing.T) {
	inc, members, areas := setup(t)
	ctx := context.Background()
	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)
	mustMember(t, members, 4, "", 0, "00000004")

	plans := NewPlanStore(inc.db)
	rallyLat1, rallyLng1 := 35.871, 128.601
	if err := plans.SaveBatch(ctx, []TeamPlanInput{
		{No: 4, Mission: "원래계획", AreaID: a1, RallyLat: &rallyLat1, RallyLng: &rallyLng1},
	}, "admin1", 1000); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	got, err := inc.Open(ctx, "유형", "메시지", []TeamInput{{No: 4, Mission: "발령임무", AreaID: a1}}, nil, "admin1", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	rallyLat2, rallyLng2 := 35.999, 128.999
	if err := plans.SaveBatch(ctx, []TeamPlanInput{
		{No: 4, Mission: "바뀐계획", AreaID: a1, RallyLat: &rallyLat2, RallyLng: &rallyLng2},
	}, "admin1", 2000); err != nil {
		t.Fatalf("re-save plan: %v", err)
	}

	tt, err := inc.GetTeamTask(ctx, got.ID, 4)
	if err != nil {
		t.Fatalf("get team task: %v", err)
	}
	if tt.RallyLat != rallyLat1 || tt.RallyLng != rallyLng1 {
		t.Errorf("rally after later plan edit = (%f,%f), want unchanged original (%f,%f)", tt.RallyLat, tt.RallyLng, rallyLat1, rallyLng1)
	}
	if tt.Mission != "발령임무" {
		t.Errorf("team_task mission = %q, want unchanged 발령임무 (plan mission only affects future drafts)", tt.Mission)
	}
}

// R25-adjacent: UpdateTeamTask (mid-incident area change) re-resolves the
// snapshot the same way Open does.
func TestUpdateTeamTask_ReResolvesSnapshot(t *testing.T) {
	inc, members, areas := setup(t)
	ctx := context.Background()
	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)
	a2 := mustArea(t, areas, "지역B", 35.90, 128.63)
	mustMember(t, members, 6, "", 0, "00000006")

	got, err := inc.Open(ctx, "유형", "메시지", []TeamInput{{No: 6, Mission: "임무", AreaID: a1}}, nil, "admin1", time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if err := inc.UpdateTeamTask(ctx, got.ID, 6, "새임무", a2, "admin1", time.Now()); err != nil {
		t.Fatalf("update team task: %v", err)
	}
	tt, err := inc.GetTeamTask(ctx, got.ID, 6)
	if err != nil {
		t.Fatalf("get team task: %v", err)
	}
	ar2, err := areas.GetByID(ctx, a2)
	if err != nil {
		t.Fatalf("get area: %v", err)
	}
	if tt.RallyLat != ar2.NavLat || tt.RallyLng != ar2.NavLng {
		t.Errorf("rally after area change = (%f,%f), want new area's nav (%f,%f)", tt.RallyLat, tt.RallyLng, ar2.NavLat, ar2.NavLng)
	}
}

// R28: BuildDraft's plan priority applies to mission and area independently
// — a plan with only a mission set doesn't force an area, and vice versa.
func TestBuildDraft_PlanPriorityIsPerField(t *testing.T) {
	db := testDB(t)
	areas := area.NewStore(db.DB)
	members := roster.NewStore(db.DB, auth.NewHasher())
	a1 := mustArea(t, areas, "다수결지역", 35.87, 128.60)

	mustMember(t, members, 8, "다수결임무", a1, "00000080")
	mustMember(t, members, 8, "다수결임무", a1, "00000081")

	plans := []TeamPlan{{TeamNo: 8, Mission: "계획임무"}} // area left at 0 (unset)
	d, err := BuildDraft(context.Background(), db.DB, plans)
	if err != nil {
		t.Fatalf("build draft: %v", err)
	}
	team := d.Teams[0]
	if team.Mission != "계획임무" || !team.FromPlan {
		t.Errorf("mission = %q fromPlan=%v, want 계획임무 from plan", team.Mission, team.FromPlan)
	}
	if team.AreaID != a1 {
		t.Errorf("area = %d, want majority-vote area %d (plan didn't set one)", team.AreaID, a1)
	}
}

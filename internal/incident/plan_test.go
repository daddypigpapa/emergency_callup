package incident

import (
	"context"
	"errors"
	"testing"

	"emergencycallup/internal/area"
)

// R24: PUT-equivalent SaveBatch then List round-trip; unsent teams stay
// empty; re-saving with fewer checkpoints drops the extras.
func TestPlanStore_SaveBatchAndList(t *testing.T) {
	db := testDB(t)
	plans := NewPlanStore(db.DB)
	areas := area.NewStore(db.DB)
	ctx := context.Background()
	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)

	lat1, lng1 := 35.871, 128.601
	err := plans.SaveBatch(ctx, []TeamPlanInput{
		{No: 1, Mission: "임무1", AreaID: a1, RallyLat: &lat1, RallyLng: &lng1, RallyAddr: "공평로 88",
			Checkpoints: []Checkpoint{{Name: "cp1", Lat: 35.8, Lng: 128.6}, {Name: "cp2", Lat: 35.81, Lng: 128.61}}},
		{No: 2, Mission: "임무2", AreaID: a1},
	}, "admin1", 1000)
	if err != nil {
		t.Fatalf("save batch: %v", err)
	}

	got, err := plans.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("List returned %d teams, want 10", len(got))
	}

	p1 := got[0]
	if p1.TeamNo != 1 || p1.Mission != "임무1" || p1.AreaID != a1 {
		t.Errorf("team 1 plan = %+v", p1)
	}
	if p1.RallyLat == nil || *p1.RallyLat != lat1 || p1.RallyLng == nil || *p1.RallyLng != lng1 {
		t.Errorf("team 1 rally = %+v, want (%f,%f)", p1, lat1, lng1)
	}
	if p1.RallyAddr != "공평로 88" {
		t.Errorf("team 1 rally addr = %q", p1.RallyAddr)
	}
	if len(p1.Checkpoints) != 2 || p1.Checkpoints[0].Seq != 1 || p1.Checkpoints[1].Seq != 2 {
		t.Errorf("team 1 checkpoints = %+v", p1.Checkpoints)
	}
	if p1.Checkpoints[0].RadiusM != 50 {
		t.Errorf("checkpoint default radius = %d, want 50", p1.Checkpoints[0].RadiusM)
	}

	p2 := got[1]
	if p2.Mission != "임무2" || p2.RallyLat != nil {
		t.Errorf("team 2 plan = %+v, want auto rally (nil)", p2)
	}

	// Teams 3..10 were never sent: zero value.
	for _, p := range got[2:] {
		if p.Mission != "" || p.AreaID != 0 || len(p.Checkpoints) != 0 {
			t.Errorf("untouched team %d plan = %+v, want zero value", p.TeamNo, p)
		}
	}

	// Re-save team 1 with only 1 checkpoint: the old 2nd checkpoint must be gone.
	if err := plans.SaveBatch(ctx, []TeamPlanInput{
		{No: 1, Mission: "임무1수정", AreaID: a1, Checkpoints: []Checkpoint{{Name: "cp-only", Lat: 35.8, Lng: 128.6}}},
	}, "admin1", 2000); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	updated, err := plans.Get(ctx, 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Mission != "임무1수정" || len(updated.Checkpoints) != 1 || updated.Checkpoints[0].Name != "cp-only" {
		t.Errorf("team 1 after re-save = %+v", updated)
	}
	// Team 2, not included in this second SaveBatch call, must be untouched.
	unaffected, err := plans.Get(ctx, 2)
	if err != nil {
		t.Fatalf("get team 2: %v", err)
	}
	if unaffected.Mission != "임무2" {
		t.Errorf("team 2 mission = %q after unrelated save, want unchanged 임무2", unaffected.Mission)
	}
}

func TestPlanStore_TooManyCheckpointsRejected(t *testing.T) {
	db := testDB(t)
	plans := NewPlanStore(db.DB)
	ctx := context.Background()

	cps := make([]Checkpoint, MaxCheckpoints+1)
	for i := range cps {
		cps[i] = Checkpoint{Name: "cp", Lat: 35.8, Lng: 128.6}
	}
	err := plans.SaveBatch(ctx, []TeamPlanInput{{No: 1, Checkpoints: cps}}, "admin1", 1000)
	if !errors.Is(err, ErrTooManyCheckpoints) {
		t.Errorf("err = %v, want ErrTooManyCheckpoints", err)
	}

	// Rejection must not have partially written anything for team 1.
	p, err := plans.Get(ctx, 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(p.Checkpoints) != 0 {
		t.Errorf("checkpoints after rejected save = %d, want 0 (all-or-nothing)", len(p.Checkpoints))
	}
}

func TestPlanStore_GetUnknownTeamReturnsZeroValue(t *testing.T) {
	db := testDB(t)
	plans := NewPlanStore(db.DB)
	p, err := plans.Get(context.Background(), 5)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.TeamNo != 5 || p.Mission != "" || p.AreaID != 0 || p.RallyLat != nil || len(p.Checkpoints) != 0 {
		t.Errorf("plan for never-saved team = %+v, want zero value", p)
	}
}

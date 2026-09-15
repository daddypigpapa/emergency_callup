package incident

import (
	"context"
	"testing"
	"time"

	"emergencycallup/internal/area"
	"emergencycallup/internal/auth"
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

func mustArea(t *testing.T, s *area.Store, name string, lat, lng float64) int64 {
	t.Helper()
	row, err := s.CreateCircle(context.Background(), name, area.Point{Lat: lat, Lng: lng}, 150, nil, time.Now())
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

// SPEC §5.4 rule 1: majority peacetime value wins; ties broken by smallest member.id.
func TestBuildDraft_MajorityAndTieBreak(t *testing.T) {
	db := testDB(t)
	areas := area.NewStore(db.DB)
	members := roster.NewStore(db.DB, auth.NewHasher())

	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)
	a2 := mustArea(t, areas, "지역B", 35.90, 128.62)

	// Team 3: two members with mission "차단선 구축"/area a1, one with a
	// different mission/area -> majority wins, minority becomes an override.
	m1 := mustMember(t, members, 3, "차단선 구축", a1, "00000001")
	_ = mustMember(t, members, 3, "차단선 구축", a1, "00000002")
	m3 := mustMember(t, members, 3, "구조 지원", a2, "00000003")

	d, err := BuildDraft(context.Background(), db.DB, nil)
	if err != nil {
		t.Fatalf("build draft: %v", err)
	}
	if len(d.Teams) != 1 {
		t.Fatalf("teams = %d, want 1", len(d.Teams))
	}
	team := d.Teams[0]
	if team.TeamNo != 3 || team.Mission != "차단선 구축" || team.AreaID != a1 {
		t.Errorf("unexpected team default: %+v", team)
	}
	if len(d.Overrides) != 1 || d.Overrides[0].MemberID != m3 {
		t.Fatalf("expected exactly one override for member %d, got %+v", m3, d.Overrides)
	}
	_ = m1
}

func TestBuildDraft_TieBreakBySmallestMemberID(t *testing.T) {
	db := testDB(t)
	areas := area.NewStore(db.DB)
	members := roster.NewStore(db.DB, auth.NewHasher())

	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)
	a2 := mustArea(t, areas, "지역B", 35.90, 128.62)

	// Exactly tied 1-vs-1: the member with the smaller ID wins (created first).
	mustMember(t, members, 5, "임무X", a1, "00000010") // smaller id
	mustMember(t, members, 5, "임무Y", a2, "00000011") // larger id

	d, err := BuildDraft(context.Background(), db.DB, nil)
	if err != nil {
		t.Fatalf("build draft: %v", err)
	}
	team := d.Teams[0]
	if team.Mission != "임무X" || team.AreaID != a1 {
		t.Errorf("tie-break failed: %+v", team)
	}
}

func TestBuildDraft_EmptyTeamFlaggedIncomplete(t *testing.T) {
	db := testDB(t)
	members := roster.NewStore(db.DB, auth.NewHasher())
	mustMember(t, members, 7, "", 0, "00000020")

	d, err := BuildDraft(context.Background(), db.DB, nil)
	if err != nil {
		t.Fatalf("build draft: %v", err)
	}
	team := d.Teams[0]
	if !team.MissionIncomplete || !team.AreaIncomplete {
		t.Errorf("expected both incomplete flags set: %+v", team)
	}
}

func TestBuildDraft_BlankMemberFollowsTeamDefault(t *testing.T) {
	db := testDB(t)
	areas := area.NewStore(db.DB)
	members := roster.NewStore(db.DB, auth.NewHasher())
	a1 := mustArea(t, areas, "지역A", 35.87, 128.60)

	mustMember(t, members, 9, "차단선 구축", a1, "00000030")
	mustMember(t, members, 9, "", 0, "00000031") // blank -> follows default, no override

	d, err := BuildDraft(context.Background(), db.DB, nil)
	if err != nil {
		t.Fatalf("build draft: %v", err)
	}
	if len(d.Overrides) != 0 {
		t.Errorf("blank-peacetime member should not become an override, got %+v", d.Overrides)
	}
}

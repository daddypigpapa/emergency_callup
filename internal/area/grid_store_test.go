package area

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStore_CreateGrid_DefaultsNavToGridCenter(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB)
	origin := CellOf(250, Point{Lat: 35.8714, Lng: 128.6014})
	cells := []Cell{{I: origin.I, J: origin.J}, {I: origin.I + 1, J: origin.J}}

	row, err := s.CreateGrid(context.Background(), "격자지역", 250, cells, nil, time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if row.Kind != KindGrid || row.GridSize != 250 || len(row.Cells) != 2 {
		t.Fatalf("row = %+v, want kind=grid size=250 2 cells", row)
	}
	want := GridCenter(250, cells)
	if row.NavLat != want.Lat || row.NavLng != want.Lng {
		t.Errorf("nav = (%f,%f), want grid center (%f,%f)", row.NavLat, row.NavLng, want.Lat, want.Lng)
	}

	// Round-trip through GetByID must preserve the cell set.
	got, err := s.GetByID(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Cells) != 2 {
		t.Fatalf("GetByID cells = %v, want 2 entries", got.Cells)
	}
}

func TestStore_CreateGrid_DedupesAndValidates(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB)
	c := CellOf(500, Point{Lat: 35.8714, Lng: 128.6014})

	// Duplicate cell in input must not be double-counted against the max.
	row, err := s.CreateGrid(context.Background(), "중복격자", 500, []Cell{c, c, c}, nil, time.Now())
	if err != nil {
		t.Fatalf("create with duplicate cells: %v", err)
	}
	if len(row.Cells) != 1 {
		t.Errorf("cells after dedupe = %d, want 1", len(row.Cells))
	}

	if _, err := s.CreateGrid(context.Background(), "잘못된크기", 300, []Cell{c}, nil, time.Now()); err == nil {
		t.Error("size 300 should be rejected")
	}
}

func TestStore_UpdateAndDeactivate_BlockedWhileInUseByActiveIncident(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB)
	ctx := context.Background()
	center := Point{Lat: 35.8714, Lng: 128.6014}

	area, err := s.CreateCircle(ctx, "사용중지역", center, 150, nil, time.Now())
	if err != nil {
		t.Fatalf("create area: %v", err)
	}

	// Seed a minimal active incident whose team_task references this area.
	if _, err := db.ExecContext(ctx, `INSERT INTO incident(id, type_text, message, status, opened_by, opened_at)
		VALUES (1, '유형', '메시지', 'active', 'tester', 1000)`); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO team_task(incident_id, team_no, mission, area_id, updated_by, updated_at)
		VALUES (1, 1, '임무', ?, 'tester', 1000)`, area.ID); err != nil {
		t.Fatalf("seed team_task: %v", err)
	}

	if _, err := s.UpdateCircle(ctx, area.ID, "새이름", center, 200, nil, time.Now()); !errors.Is(err, ErrAreaInUse) {
		t.Errorf("UpdateCircle on in-use area: err = %v, want ErrAreaInUse", err)
	}
	if err := s.Deactivate(ctx, area.ID); !errors.Is(err, ErrAreaInUse) {
		t.Errorf("Deactivate on in-use area: err = %v, want ErrAreaInUse", err)
	}

	// Close the incident: the same operations should now succeed.
	if _, err := db.ExecContext(ctx, `UPDATE incident SET status='closed' WHERE id=1`); err != nil {
		t.Fatalf("close incident: %v", err)
	}
	if _, err := s.UpdateCircle(ctx, area.ID, "새이름", center, 200, nil, time.Now()); err != nil {
		t.Errorf("UpdateCircle after incident closed: %v", err)
	}
}

func TestStore_Deactivate_BlockedWhileReferencedByMember(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB)
	ctx := context.Background()

	area, err := s.CreateCircle(ctx, "참조지역", Point{Lat: 35.8714, Lng: 128.6014}, 150, nil, time.Now())
	if err != nil {
		t.Fatalf("create area: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO member(id, login_id, pw_hash, dept, name, mobile, team_no, area_id, active, created_at, updated_at)
		VALUES (1, 'm1', 'x', '부서', '이름', '01011112222', 1, ?, 1, 1000, 1000)`, area.ID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	err = s.Deactivate(ctx, area.ID)
	if !errors.Is(err, ErrAreaReferenced) {
		t.Errorf("Deactivate on member-referenced area: err = %v, want ErrAreaReferenced", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE member SET area_id = NULL WHERE id = 1`); err != nil {
		t.Fatalf("clear member area_id: %v", err)
	}
	if err := s.Deactivate(ctx, area.ID); err != nil {
		t.Errorf("Deactivate after clearing member reference: %v", err)
	}
}

func TestStore_UpdateGrid_ChangesKindFromCircle(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB)
	ctx := context.Background()

	area, err := s.CreateCircle(ctx, "원래원", Point{Lat: 35.8714, Lng: 128.6014}, 150, nil, time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	origin := CellOf(250, Point{Lat: 35.8714, Lng: 128.6014})
	updated, err := s.UpdateGrid(ctx, area.ID, "이제격자", 250, []Cell{{I: origin.I, J: origin.J}}, nil, time.Now())
	if err != nil {
		t.Fatalf("update to grid: %v", err)
	}
	if updated.Kind != KindGrid || updated.RadiusM != 0 {
		t.Errorf("row after UpdateGrid = %+v, want kind=grid and no leftover radius", updated)
	}
	got, err := s.GetByID(ctx, area.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Kind != KindGrid || len(got.Cells) != 1 {
		t.Errorf("persisted row = %+v, want kind=grid with 1 cell", got)
	}
}

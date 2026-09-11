package area

import (
	"context"
	"testing"
	"time"

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

func TestStore_CreateCircle_DefaultsNavToCenter(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB)
	center := Point{Lat: 35.8714, Lng: 128.6014}
	row, err := s.CreateCircle(context.Background(), "신천교 북단", center, 150, nil, time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if row.NavLat != center.Lat || row.NavLng != center.Lng {
		t.Errorf("nav = (%f,%f), want center (%f,%f)", row.NavLat, row.NavLng, center.Lat, center.Lng)
	}
}

func TestStore_CreatePolygon_NavOutsideRejectedWithoutExplicit(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB)
	// An L-shaped (concave) polygon whose vertex-centroid falls in the
	// missing notch, i.e. outside the polygon.
	poly := []Point{
		{Lat: 35.870, Lng: 128.600},
		{Lat: 35.870, Lng: 128.610},
		{Lat: 35.874, Lng: 128.610},
		{Lat: 35.874, Lng: 128.604},
		{Lat: 35.880, Lng: 128.604},
		{Lat: 35.880, Lng: 128.600},
	}
	_, err := s.CreatePolygon(context.Background(), "L자 구역", poly, nil, time.Now())
	if err != ErrNavOutside {
		t.Fatalf("expected ErrNavOutside, got %v", err)
	}

	// Providing an explicit nav point should succeed.
	explicitNav := Point{Lat: 35.871, Lng: 128.601}
	row, err := s.CreatePolygon(context.Background(), "L자 구역", poly, &explicitNav, time.Now())
	if err != nil {
		t.Fatalf("create with explicit nav: %v", err)
	}
	if row.NavLat != explicitNav.Lat {
		t.Errorf("nav lat = %f, want %f", row.NavLat, explicitNav.Lat)
	}
}

func TestStore_DuplicateActiveNameRejected(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB)
	ctx := context.Background()
	center := Point{Lat: 35.8714, Lng: 128.6014}
	if _, err := s.CreateCircle(ctx, "dup", center, 150, nil, time.Now()); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := s.CreateCircle(ctx, "dup", center, 200, nil, time.Now())
	if err != ErrNameTaken {
		t.Fatalf("expected ErrNameTaken, got %v", err)
	}
}

// Used areas are never mutated: Copy creates a new row and deactivates the source.
func TestStore_CopyCircle_DeactivatesSource(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB)
	ctx := context.Background()
	center := Point{Lat: 35.8714, Lng: 128.6014}
	orig, err := s.CreateCircle(ctx, "원본", center, 150, nil, time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	newCenter := Point{Lat: 35.8720, Lng: 128.6020}
	copied, err := s.CopyCircle(ctx, orig.ID, "원본-v2", newCenter, 200, nil, time.Now())
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if !copied.Active {
		t.Error("copy should be active")
	}

	origAfter, err := s.GetByID(ctx, orig.ID)
	if err != nil {
		t.Fatalf("get orig: %v", err)
	}
	if origAfter.Active {
		t.Error("original should be deactivated after copy")
	}
	// Original geometry must be unchanged (never mutated in place).
	if origAfter.Lat != center.Lat || origAfter.RadiusM != 150 {
		t.Errorf("original geometry changed: %+v", origAfter)
	}
}

func TestStore_GetActiveByName_UsedByRosterImport(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB)
	ctx := context.Background()
	center := Point{Lat: 35.8714, Lng: 128.6014}
	if _, err := s.CreateCircle(ctx, "신천교 북단", center, 150, nil, time.Now()); err != nil {
		t.Fatalf("create: %v", err)
	}
	row, err := s.GetActiveByName(ctx, "신천교 북단")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if row.Name != "신천교 북단" {
		t.Errorf("name = %q", row.Name)
	}
	if _, err := s.GetActiveByName(ctx, "없는지역"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

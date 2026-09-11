package settings

import (
	"context"
	"testing"
	"time"

	"emergencycallup/internal/store"
)

func TestStore_GetUnsetReturnsErrNotSet(t *testing.T) {
	db, err := store.OpenMemory(t.Name())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	s := NewStore(db.DB)

	if _, err := s.Get(context.Background(), TileKeyName); err != ErrNotSet {
		t.Fatalf("expected ErrNotSet, got %v", err)
	}
}

func TestStore_SetThenGet(t *testing.T) {
	db, err := store.OpenMemory(t.Name())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	s := NewStore(db.DB)
	ctx := context.Background()

	if err := s.Set(ctx, TileKeyName, "ABCD-1234", "admin:a1", time.Now()); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.Get(ctx, TileKeyName)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "ABCD-1234" {
		t.Errorf("got %q, want ABCD-1234", got)
	}

	// Overwrite.
	if err := s.Set(ctx, TileKeyName, "NEW-KEY", "admin:a1", time.Now()); err != nil {
		t.Fatalf("set again: %v", err)
	}
	got, err = s.Get(ctx, TileKeyName)
	if err != nil {
		t.Fatalf("get after overwrite: %v", err)
	}
	if got != "NEW-KEY" {
		t.Errorf("got %q, want NEW-KEY", got)
	}
}

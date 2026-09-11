package auth

import (
	"context"
	"testing"
	"time"
)

func TestAdminStore_CreateAndFind(t *testing.T) {
	db := testDB(t)
	h := NewHasher()
	s := NewAdminStore(db.DB, h)
	ctx := context.Background()
	now := time.Now()

	u, err := s.Create(ctx, "admin1", "strongpass1", "관리자", "안전정책과", RoleAdmin, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("expected non-zero id")
	}

	found, err := s.FindByLoginID(ctx, "admin1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	ok, err := h.Verify(found.PWHash, "strongpass1")
	if err != nil || !ok {
		t.Fatalf("password should verify: ok=%v err=%v", ok, err)
	}
}

// R6: empty DB must have zero loginable accounts.
func TestAdminStore_EmptyDBHasNoAccounts(t *testing.T) {
	db := testDB(t)
	s := NewAdminStore(db.DB, NewHasher())
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 accounts in fresh DB, got %d", len(list))
	}
}

func TestAdminStore_DuplicateLoginIDRejected(t *testing.T) {
	db := testDB(t)
	s := NewAdminStore(db.DB, NewHasher())
	ctx := context.Background()
	now := time.Now()
	if _, err := s.Create(ctx, "dup1", "strongpass1", "A", "", RoleAdmin, now); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := s.Create(ctx, "dup1", "strongpass2", "B", "", RoleOperator, now)
	if err != ErrAdminLoginIDTaken {
		t.Fatalf("expected ErrAdminLoginIDTaken, got %v", err)
	}
}

func TestAdminStore_CannotDeactivateLastActiveAdmin(t *testing.T) {
	db := testDB(t)
	s := NewAdminStore(db.DB, NewHasher())
	ctx := context.Background()
	now := time.Now()

	a, err := s.Create(ctx, "sole-admin", "strongpass1", "A", "", RoleAdmin, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetActive(ctx, a.ID, false); err != ErrLastActiveAdmin {
		t.Fatalf("expected ErrLastActiveAdmin, got %v", err)
	}

	// Adding a second admin should allow deactivating the first.
	b, err := s.Create(ctx, "second-admin", "strongpass1", "B", "", RoleAdmin, now)
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if err := s.SetActive(ctx, a.ID, false); err != nil {
		t.Fatalf("should be able to deactivate once a second admin exists: %v", err)
	}
	_ = b
}

func TestAdminStore_WeakPasswordRejected(t *testing.T) {
	db := testDB(t)
	s := NewAdminStore(db.DB, NewHasher())
	_, err := s.Create(context.Background(), "u1", "short", "A", "", RoleAdmin, time.Now())
	if err != ErrPasswordTooShort {
		t.Fatalf("expected ErrPasswordTooShort, got %v", err)
	}
}

package roster

import (
	"context"
	"testing"
	"time"

	"emergencycallup/internal/auth"
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

func sampleInput() NewInput {
	return NewInput{
		LoginID: "k1234", Dept: "안전정책과", Name: "김도현",
		OfficeTel: "1234", Mobile: "01011112222", TeamNo: 3,
		Mission: "차단선 구축", Note: "야간 대기조",
	}
}

func TestStore_CreateAndGet(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB, auth.NewHasher())
	ctx := context.Background()

	m, pw, err := s.Create(ctx, sampleInput(), time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(pw) != auth.RandomPasswordLength {
		t.Errorf("password length = %d, want %d", len(pw), auth.RandomPasswordLength)
	}
	if !m.PWMustChange {
		t.Error("new member should have pw_must_change=1")
	}

	got, err := s.GetByLoginID(ctx, "k1234")
	if err != nil {
		t.Fatalf("get by login id: %v", err)
	}
	if got.Name != "김도현" || got.Mobile != "01011112222" {
		t.Errorf("unexpected member: %+v", got)
	}
}

func TestStore_DuplicateLoginIDRejected(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB, auth.NewHasher())
	ctx := context.Background()
	if _, _, err := s.Create(ctx, sampleInput(), time.Now()); err != nil {
		t.Fatalf("first create: %v", err)
	}
	in2 := sampleInput()
	in2.Mobile = "01099998888"
	_, _, err := s.Create(ctx, in2, time.Now())
	if err != ErrLoginIDTaken {
		t.Fatalf("expected ErrLoginIDTaken, got %v", err)
	}
}

func TestStore_DuplicateMobileRejected(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB, auth.NewHasher())
	ctx := context.Background()
	if _, _, err := s.Create(ctx, sampleInput(), time.Now()); err != nil {
		t.Fatalf("first create: %v", err)
	}
	in2 := sampleInput()
	in2.LoginID = "other1"
	_, _, err := s.Create(ctx, in2, time.Now())
	if err != ErrMobileTaken {
		t.Fatalf("expected ErrMobileTaken, got %v", err)
	}
}

func TestStore_GetByMobile(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB, auth.NewHasher())
	ctx := context.Background()
	if _, _, err := s.Create(ctx, sampleInput(), time.Now()); err != nil {
		t.Fatalf("create: %v", err)
	}
	m, err := s.GetByMobile(ctx, "01011112222")
	if err != nil {
		t.Fatalf("get by mobile: %v", err)
	}
	if m.LoginID != "k1234" {
		t.Errorf("login id = %q", m.LoginID)
	}
}

func TestStore_SetActive(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB, auth.NewHasher())
	ctx := context.Background()
	m, _, err := s.Create(ctx, sampleInput(), time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetActive(ctx, m.ID, false, time.Now()); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	got, err := s.GetByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Active {
		t.Error("member should be inactive")
	}
}

func TestStore_Update(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB, auth.NewHasher())
	ctx := context.Background()
	m, _, err := s.Create(ctx, sampleInput(), time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	newName := "김철수"
	if err := s.Update(ctx, m.ID, UpdateInput{Name: &newName}, time.Now()); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.GetByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "김철수" {
		t.Errorf("name = %q, want 김철수", got.Name)
	}
	if got.Mobile != m.Mobile {
		t.Error("unrelated field mobile should be unchanged")
	}
}

func TestStore_PasswordReset(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB, auth.NewHasher())
	ctx := context.Background()
	m, oldPw, err := s.Create(ctx, sampleInput(), time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	newPw, err := s.PasswordReset(ctx, m.ID)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if newPw == oldPw {
		t.Error("reset password should differ from original")
	}
	got, err := s.GetByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.PWMustChange {
		t.Error("reset should set pw_must_change")
	}
}

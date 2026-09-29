package roster

import (
	"context"
	"strings"
	"testing"
	"time"

	"emergencycallup/internal/auth"
)

func TestParseTestAccounts(t *testing.T) {
	in := bom + "# 테스트 계정\n\ntest01=devpass456\n  test02 = devpass456  \nbroken-line\n=nopw\nnoid=\ntest03=pa=ss=word1\n"
	accounts, problems, err := ParseTestAccounts(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []TestAccount{
		{Line: 3, LoginID: "test01", Password: "devpass456"},
		{Line: 4, LoginID: "test02", Password: "devpass456"},
		{Line: 8, LoginID: "test03", Password: "pa=ss=word1"},
	}
	if len(accounts) != len(want) {
		t.Fatalf("accounts = %+v, want %+v", accounts, want)
	}
	for i := range want {
		if accounts[i] != want[i] {
			t.Errorf("accounts[%d] = %+v, want %+v", i, accounts[i], want[i])
		}
	}
	if len(problems) != 3 {
		t.Fatalf("problems = %v, want 3 (lines 5, 6, 7)", problems)
	}
	for _, p := range problems {
		if strings.Contains(p, "nopw") || strings.Contains(p, "broken") {
			t.Errorf("problem %q echoes line content (could leak a password to the log)", p)
		}
	}
}

func TestApplyTestAccounts(t *testing.T) {
	db := testDB(t)
	h := auth.NewHasher()
	s := NewStore(db.DB, h)
	ctx := context.Background()

	m, _, err := s.Create(ctx, sampleInput(), time.Now()) // login id k1234, must-change on
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	applied, problems := s.ApplyTestAccounts(ctx, []TestAccount{
		{Line: 1, LoginID: "k1234", Password: "devpass456"},
		{Line: 2, LoginID: "nobody", Password: "devpass456"},
		{Line: 3, LoginID: "k1234", Password: "short"},
	})
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}
	if len(problems) != 2 {
		t.Fatalf("problems = %v, want 2 (unknown id, policy)", problems)
	}

	got, err := s.GetByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok, _ := h.Verify(got.PWHash, "devpass456"); !ok {
		t.Error("password from the file was not applied")
	}
	if got.PWMustChange {
		t.Error("test accounts should not be forced to change password on login")
	}
}

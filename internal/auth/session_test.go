package auth

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
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSessionStore_CreateAndValidate(t *testing.T) {
	db := testDB(t)
	ss := NewSessionStore(db.DB)
	ctx := context.Background()
	now := time.Now()

	raw, err := ss.Create(ctx, KindMember, 42, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sess, err := ss.Validate(ctx, raw, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if sess.SubjectID != 42 || sess.Kind != KindMember {
		t.Errorf("unexpected session: %+v", sess)
	}
}

// R11: garbage/invalid token must fail cleanly (mapped to 401 by httpapi),
// never panic or 500.
func TestSessionStore_InvalidToken(t *testing.T) {
	db := testDB(t)
	ss := NewSessionStore(db.DB)
	ctx := context.Background()

	_, err := ss.Validate(ctx, "%E0%A4%A-not-a-real-token", time.Now())
	if err != ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestSessionStore_MemberExpiresAfter24h(t *testing.T) {
	db := testDB(t)
	ss := NewSessionStore(db.DB)
	ctx := context.Background()
	now := time.Now()

	raw, err := ss.Create(ctx, KindMember, 1, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := ss.Validate(ctx, raw, now.Add(24*time.Hour+time.Second)); err != ErrSessionExpired {
		t.Fatalf("expected ErrSessionExpired after 24h, got %v", err)
	}
}

func TestSessionStore_AdminIdleTimeout(t *testing.T) {
	db := testDB(t)
	ss := NewSessionStore(db.DB)
	ctx := context.Background()
	now := time.Now()

	raw, err := ss.Create(ctx, KindAdmin, 1, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Still within 12h absolute cap, but idle for > 2h since last_seen_at.
	if _, err := ss.Validate(ctx, raw, now.Add(2*time.Hour+time.Minute)); err != ErrSessionExpired {
		t.Fatalf("expected idle timeout expiry, got %v", err)
	}
}

func TestSessionStore_AdminActivityResetsIdle(t *testing.T) {
	db := testDB(t)
	ss := NewSessionStore(db.DB)
	ctx := context.Background()
	now := time.Now()

	raw, err := ss.Create(ctx, KindAdmin, 1, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Touch the session at +1h (still active), then check +2h31m from
	// creation (only 1h31m idle since last touch) — should still be valid.
	if _, err := ss.Validate(ctx, raw, now.Add(time.Hour)); err != nil {
		t.Fatalf("validate at +1h: %v", err)
	}
	if _, err := ss.Validate(ctx, raw, now.Add(2*time.Hour+31*time.Minute)); err != nil {
		t.Fatalf("expected still valid due to reset idle clock, got %v", err)
	}
}

func TestSessionStore_RevokeAllForSubject(t *testing.T) {
	db := testDB(t)
	ss := NewSessionStore(db.DB)
	ctx := context.Background()
	now := time.Now()

	raw1, _ := ss.Create(ctx, KindMember, 7, now)
	raw2, _ := ss.Create(ctx, KindMember, 7, now)

	if err := ss.RevokeAllForSubject(ctx, KindMember, 7); err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if _, err := ss.Validate(ctx, raw1, now); err != ErrSessionNotFound {
		t.Errorf("session 1 should be gone, got %v", err)
	}
	if _, err := ss.Validate(ctx, raw2, now); err != ErrSessionNotFound {
		t.Errorf("session 2 should be gone, got %v", err)
	}
}

func TestSessionStore_ExpireMemberSessionsBy(t *testing.T) {
	db := testDB(t)
	ss := NewSessionStore(db.DB)
	ctx := context.Background()
	now := time.Now()

	raw, _ := ss.Create(ctx, KindMember, 1, now) // expires at now+24h

	cutoff := now.Add(time.Hour) // incident closed now, +1h cutoff
	if err := ss.ExpireMemberSessionsBy(ctx, cutoff.UnixMilli()); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, err := ss.Validate(ctx, raw, cutoff.Add(time.Minute)); err != ErrSessionExpired {
		t.Fatalf("expected session capped at incident-close+1h, got %v", err)
	}
}

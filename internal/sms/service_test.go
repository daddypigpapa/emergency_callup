package sms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"emergencycallup/internal/audit"
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

func seedIncidentAndMembers(t *testing.T, db *store.DB) (incidentID int64, memberIDs []int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	res, err := db.Exec(`INSERT INTO incident(type_text, message, status, version, opened_by, opened_at) VALUES ('T','M','active',1,'admin:a',?)`, now)
	if err != nil {
		t.Fatalf("insert incident: %v", err)
	}
	incidentID, _ = res.LastInsertId()
	for i := 0; i < 3; i++ {
		mres, err := db.Exec(`INSERT INTO member(login_id, pw_hash, dept, name, mobile, team_no, active, created_at, updated_at)
			VALUES (?, 'x', 'dept', ?, ?, 1, 1, ?, ?)`,
			"login"+itoa(i), "이름"+itoa(i), "0101111000"+itoa(i), now, now)
		if err != nil {
			t.Fatalf("insert member: %v", err)
		}
		id, _ := mres.LastInsertId()
		memberIDs = append(memberIDs, id)
	}
	return incidentID, memberIDs
}

func itoa(i int) string {
	return string(rune('0' + i))
}

func TestPrepare_Manual(t *testing.T) {
	db := testDB(t)
	svc := NewService(db.DB, nil, audit.New(db.DB))
	incidentID, memberIDs := seedIncidentAndMembers(t, db)

	var recipients []Message
	for _, id := range memberIDs {
		recipients = append(recipients, Message{MemberID: id, Mobile: "01011110000", Text: "text"})
	}
	res, err := svc.Prepare(context.Background(), incidentID, KindOpen, "manual", "본문", recipients, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if res.Count != 3 {
		t.Errorf("count = %d, want 3", res.Count)
	}

	batch, err := svc.GetBatchResult(context.Background(), res.BatchID)
	if err != nil {
		t.Fatalf("get batch: %v", err)
	}
	if batch.Pending != 3 {
		t.Errorf("pending = %d, want 3 (manual doesn't auto-send)", batch.Pending)
	}

	if err := svc.MarkSent(context.Background(), res.BatchID, "admin:a", time.Now()); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	batch, err = svc.GetBatchResult(context.Background(), res.BatchID)
	if err != nil {
		t.Fatalf("get batch after mark-sent: %v", err)
	}
	if batch.Sent != 3 {
		t.Errorf("sent = %d, want 3 after mark-sent", batch.Sent)
	}
}

func TestPrepare_HTTPDisabledByDefault(t *testing.T) {
	db := testDB(t)
	svc := NewService(db.DB, nil, audit.New(db.DB))
	incidentID, memberIDs := seedIncidentAndMembers(t, db)
	_, err := svc.Prepare(context.Background(), incidentID, KindOpen, "http", "본문",
		[]Message{{MemberID: memberIDs[0], Mobile: "01011110000"}}, "admin:a", time.Now())
	if err != ErrHTTPDisabled {
		t.Fatalf("expected ErrHTTPDisabled, got %v", err)
	}
}

func TestPrepare_HTTPSuccessAndFailure(t *testing.T) {
	db := testDB(t)
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider, err := NewHTTPProvider(server.URL, "Authorization: Bearer x", `{"to":"{{.Mobile}}","msg":"{{.Text}}"}`, "sender", 50)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	provider.RetryDelay = 10 * time.Millisecond
	provider.MaxRetries = 1
	svc := NewService(db.DB, provider, audit.New(db.DB))
	incidentID, memberIDs := seedIncidentAndMembers(t, db)

	var recipients []Message
	for _, id := range memberIDs {
		recipients = append(recipients, Message{MemberID: id, Mobile: "01011110000", Text: "hello"})
	}
	res, err := svc.Prepare(context.Background(), incidentID, KindOpen, "http", "본문", recipients, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if res.Count != 3 {
		t.Errorf("count = %d, want 3", res.Count)
	}

	// Wait for background retry goroutine to finish and re-check.
	time.Sleep(200 * time.Millisecond)
	batch, err := svc.GetBatchResult(context.Background(), res.BatchID)
	if err != nil {
		t.Fatalf("get batch: %v", err)
	}
	if batch.Sent+batch.Failed != 3 {
		t.Errorf("sent+failed = %d, want 3 (batch: %+v)", batch.Sent+batch.Failed, batch)
	}
}

func TestLastFailedMemberIDs(t *testing.T) {
	db := testDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	provider, err := NewHTTPProvider(server.URL, "", `{"to":"{{.Mobile}}"}`, "s", 50)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	provider.MaxRetries = 0
	svc := NewService(db.DB, provider, audit.New(db.DB))
	incidentID, memberIDs := seedIncidentAndMembers(t, db)

	res, err := svc.Prepare(context.Background(), incidentID, KindOpen, "http", "본문",
		[]Message{{MemberID: memberIDs[0], Mobile: "01011110000"}}, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	failed, err := svc.LastFailedMemberIDs(context.Background(), incidentID, KindOpen)
	if err != nil {
		t.Fatalf("last failed: %v", err)
	}
	if len(failed) != 1 || failed[0] != memberIDs[0] {
		t.Errorf("failed = %v, want [%d]", failed, memberIDs[0])
	}
	_ = res
}

func TestRecipientsCSVRows(t *testing.T) {
	db := testDB(t)
	svc := NewService(db.DB, nil, audit.New(db.DB))
	incidentID, memberIDs := seedIncidentAndMembers(t, db)
	res, err := svc.Prepare(context.Background(), incidentID, KindOpen, "manual", "본문",
		[]Message{{MemberID: memberIDs[0], Mobile: "01011110000"}}, "admin:a", time.Now())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	rows, err := svc.RecipientsCSVRows(context.Background(), res.BatchID)
	if err != nil {
		t.Fatalf("csv rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Mobile != "01011110000" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestTemplates(t *testing.T) {
	open := OpenText("https://mob.example.go.kr", "비상 2단계", "신천 수위 상승")
	if !containsAll(open, "[비상 2단계]", "신천 수위 상승", "https://mob.example.go.kr/f") {
		t.Errorf("open text malformed: %s", open)
	}
	closeText := CloseText("비상 2단계")
	if !containsAll(closeText, "[상황종료]", "비상 2단계") {
		t.Errorf("close text malformed: %s", closeText)
	}
	resend := ResendText("https://mob.example.go.kr", "비상 2단계")
	if !containsAll(resend, "재안내", "https://mob.example.go.kr/f") {
		t.Errorf("resend text malformed: %s", resend)
	}
}

func TestIsLMS(t *testing.T) {
	short := "짧은 문자"
	if IsLMS(short) {
		t.Errorf("short text should not be LMS")
	}
	long := ""
	for i := 0; i < 60; i++ {
		long += "가"
	}
	if !IsLMS(long) {
		t.Errorf("60 Korean chars (120 EUC-KR bytes) should be LMS")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

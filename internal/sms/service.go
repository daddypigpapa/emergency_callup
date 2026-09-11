package sms

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"emergencycallup/internal/audit"
)

const (
	KindOpen   = "open"
	KindClose  = "close"
	KindResend = "resend"
)

var ErrHTTPDisabled = errors.New("sms: http provider is not configured")

// Service orchestrates batch creation/tracking in `sms_log` (SPEC §4, §9.3, §9.4).
type Service struct {
	db     *sql.DB
	manual Provider
	http   Provider // nil when SMS_HTTP_URL is unset (SPEC §9.1)
	audit  *audit.Log
}

func NewService(db *sql.DB, httpProvider Provider, a *audit.Log) *Service {
	return &Service{db: db, manual: ManualProvider{}, http: httpProvider, audit: a}
}

func (s *Service) HTTPEnabled() bool { return s.http != nil }

func newBatchID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// PrepareResult is the POST /a/incidents/{id}/sms response payload (SPEC §7.3).
type PrepareResult struct {
	BatchID string
	Text    string
	Count   int
	Bytes   int
	LMS     bool
}

// Prepare creates a batch: one `sms_log` row per recipient, then (for
// via="http") makes the first delivery pass synchronously and kicks off
// background retries for any failures (see HTTPProvider.Send's doc
// comment). via="manual" only records rows — actual sending is external.
func (s *Service) Prepare(ctx context.Context, incidentID int64, kind, via, text string, recipients []Message, actor string, now time.Time) (*PrepareResult, error) {
	if via != "manual" && via != "http" {
		return nil, errors.New("sms: via must be manual or http")
	}
	if via == "http" && s.http == nil {
		return nil, ErrHTTPDisabled
	}

	batchID := newBatchID()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	nowMs := now.UnixMilli()
	for _, r := range recipients {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sms_log(batch_id, incident_id, kind, member_id, provider, status, ts) VALUES (?, ?, ?, ?, ?, 'prepared', ?)`,
			batchID, incidentID, kind, r.MemberID, via, nowMs); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = s.audit.Record(ctx, now, audit.Event{Type: "sms_prepare", Actor: actor, IncidentID: &incidentID,
		Data: map[string]any{"batchId": batchID, "kind": kind, "via": via, "count": len(recipients)}})

	if via == "http" {
		results := s.http.Send(ctx, recipients)
		var failed []Message
		byMember := make(map[int64]Message, len(recipients))
		for _, r := range recipients {
			byMember[r.MemberID] = r
		}
		for _, r := range results {
			if err := s.recordResult(ctx, batchID, r); err != nil {
				return nil, err
			}
			if !r.OK {
				failed = append(failed, byMember[r.MemberID])
			}
		}
		if len(failed) > 0 {
			if hp, ok := s.http.(*HTTPProvider); ok {
				bg := context.Background()
				go hp.SendWithRetries(bg, failed, func(r Result) {
					_ = s.recordResult(bg, batchID, r)
				})
			}
		}
	}

	_ = s.audit.Record(ctx, now, audit.Event{Type: "sms_send", Actor: actor, IncidentID: &incidentID,
		Data: map[string]any{"batchId": batchID}})

	return &PrepareResult{BatchID: batchID, Text: text, Count: len(recipients), Bytes: euckrLen(text), LMS: IsLMS(text)}, nil
}

func (s *Service) recordResult(ctx context.Context, batchID string, r Result) error {
	status := "failed"
	if r.OK {
		status = "sent"
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE sms_log SET status=?, provider_ref=? WHERE batch_id=? AND member_id=?`,
		status, r.Ref, batchID, r.MemberID)
	return err
}

// MarkSent implements POST .../sms/{batchId}/mark-sent: the operator
// confirms they sent a manual batch through the agency's own system.
func (s *Service) MarkSent(ctx context.Context, batchID, actor string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sms_log SET status='sent' WHERE batch_id=? AND provider='manual'`, batchID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	_ = s.audit.Record(ctx, now, audit.Event{Type: "sms_mark_sent", Actor: actor, Data: map[string]any{"batchId": batchID}})
	return nil
}

// BatchResult summarizes one batch (SPEC §7.3 GET .../sms/{batchId}).
type BatchResult struct {
	BatchID       string
	Sent          int
	Failed        int
	Pending       int
	FailedMembers []int64
}

func (s *Service) GetBatchResult(ctx context.Context, batchID string) (*BatchResult, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT member_id, status FROM sms_log WHERE batch_id=?`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := &BatchResult{BatchID: batchID}
	found := false
	for rows.Next() {
		found = true
		var memberID int64
		var status string
		if err := rows.Scan(&memberID, &status); err != nil {
			return nil, err
		}
		switch status {
		case "sent":
			res.Sent++
		case "failed":
			res.Failed++
			res.FailedMembers = append(res.FailedMembers, memberID)
		default:
			res.Pending++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, sql.ErrNoRows
	}
	return res, nil
}

// Recipient is one row for the manual-send CSV export (SPEC §9.3, §8.2.5).
type Recipient struct {
	Name   string
	Mobile string
}

// RecipientsCSVRows returns name+mobile for every recipient of a batch,
// joined against the member table (SPEC: "이름,휴대전화").
func (s *Service) RecipientsCSVRows(ctx context.Context, batchID string) ([]Recipient, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.name, m.mobile FROM sms_log s JOIN member m ON m.id = s.member_id WHERE s.batch_id = ? ORDER BY m.name`,
		batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recipient
	for rows.Next() {
		var r Recipient
		if err := rows.Scan(&r.Name, &r.Mobile); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastFailedMemberIDs implements the "failed" target filter (SPEC §9.5):
// members who failed in the most recent http batch of this kind for this incident.
func (s *Service) LastFailedMemberIDs(ctx context.Context, incidentID int64, kind string) ([]int64, error) {
	var lastBatch string
	err := s.db.QueryRowContext(ctx,
		`SELECT batch_id FROM sms_log WHERE incident_id=? AND kind=? AND provider='http' ORDER BY ts DESC LIMIT 1`,
		incidentID, kind).Scan(&lastBatch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT member_id FROM sms_log WHERE batch_id=? AND status='failed'`, lastBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

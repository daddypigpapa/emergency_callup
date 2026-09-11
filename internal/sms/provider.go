// Package sms implements SPEC §9's pluggable SMS dispatch: a "manual"
// provider (always available — the operator sends via an existing agency
// system) and an optional "http" provider (a REST SMS gateway).
package sms

import "context"

// Message is one outbound text (SPEC §9.1).
type Message struct {
	MemberID int64
	Mobile   string
	Text     string
}

// Result is one delivery outcome.
type Result struct {
	MemberID int64
	OK       bool
	Ref      string
	Err      string
}

// Provider is the pluggable dispatch interface (SPEC §9.1). Adding a new
// SMS vendor means implementing exactly this interface once.
type Provider interface {
	Name() string
	Send(ctx context.Context, msgs []Message) []Result
}

// ManualProvider represents dispatch through the agency's existing SMS
// system: this process never talks to a phone network itself. Send()
// immediately reports every message "OK" — that only means the batch is
// ready for the operator to copy/export (SPEC §9.3); actual delivery
// confirmation comes later from the operator via
// POST .../sms/{batchId}/mark-sent.
type ManualProvider struct{}

func (ManualProvider) Name() string { return "manual" }

func (ManualProvider) Send(_ context.Context, msgs []Message) []Result {
	out := make([]Result, len(msgs))
	for i, m := range msgs {
		out[i] = Result{MemberID: m.MemberID, OK: true}
	}
	return out
}

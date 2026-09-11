package sms

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"text/template"
	"time"
)

// TemplateData is what SMS_HTTP_BODY_TEMPLATE can reference (SPEC §9.4:
// "필드 .Mobile .Text .Sender").
type TemplateData struct {
	Mobile string
	Text   string
	Sender string
}

// HTTPProvider posts one request per message to an SMS gateway (SPEC §9.4).
type HTTPProvider struct {
	URL          string
	AuthHeader   string // raw "Header-Name: value", split once at the first ':'
	BodyTemplate *template.Template
	Sender       string
	RPS          int
	Timeout      time.Duration // default 10s
	RetryDelay   time.Duration // default 30s; tests may shrink this
	MaxRetries   int           // default 2 (SPEC §9.4: "실패 건 2회 재시도")

	Client *http.Client
}

// NewHTTPProvider parses SMS_HTTP_BODY_TEMPLATE and builds a ready provider.
func NewHTTPProvider(url, authHeader, bodyTemplate, sender string, rps int) (*HTTPProvider, error) {
	tmpl, err := template.New("sms_body").Parse(bodyTemplate)
	if err != nil {
		return nil, err
	}
	return &HTTPProvider{
		URL: url, AuthHeader: authHeader, BodyTemplate: tmpl, Sender: sender, RPS: rps,
		Timeout: 10 * time.Second, RetryDelay: 30 * time.Second, MaxRetries: 2,
		Client: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (p *HTTPProvider) Name() string { return "http" }

// Send makes one delivery attempt per message, respecting SMS_HTTP_RPS, and
// returns immediately with those first-pass results — matching SPEC §7.3's
// synchronous POST /a/incidents/{id}/sms response ("http면 결과 수 포함").
//
// DECISION: SPEC §9.4's "실패 건 2회 재시도(30초 간격)" would otherwise
// force the admin's HTTP request to block up to ~60s per failing
// recipient. Retries instead run in the background (SendWithRetries) and
// update sms_log asynchronously; the admin sees the first-pass count right
// away and can also use the documented [실패자 재발송] action, and later
// results via GET .../sms/{batchId}.
func (p *HTTPProvider) Send(ctx context.Context, msgs []Message) []Result {
	return p.sendOnePass(ctx, msgs)
}

func (p *HTTPProvider) sendOnePass(ctx context.Context, msgs []Message) []Result {
	rps := p.RPS
	if rps <= 0 {
		rps = 10
	}
	interval := time.Second / time.Duration(rps)
	out := make([]Result, len(msgs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, rps) // cap in-flight to the rate as a simple throttle
	for i, m := range msgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, m Message) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = p.sendOne(ctx, m)
		}(i, m)
		if interval > 0 {
			time.Sleep(interval)
		}
	}
	wg.Wait()
	return out
}

// SendWithRetries runs the first pass, then retries only the failures up to
// MaxRetries times with RetryDelay between attempts, invoking onUpdate for
// every result that changes (including retries) so the caller can persist
// sms_log incrementally. It's meant to be run in a background goroutine.
func (p *HTTPProvider) SendWithRetries(ctx context.Context, msgs []Message, onUpdate func(Result)) {
	pending := msgs
	for attempt := 0; ; attempt++ {
		results := p.sendOnePass(ctx, pending)
		var next []Message
		for i, r := range results {
			onUpdate(r)
			if !r.OK {
				next = append(next, pending[i])
			}
		}
		if len(next) == 0 || attempt >= p.MaxRetries {
			return
		}
		pending = next
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.retryDelay()):
		}
	}
}

func (p *HTTPProvider) retryDelay() time.Duration {
	if p.RetryDelay <= 0 {
		return 30 * time.Second
	}
	return p.RetryDelay
}

func (p *HTTPProvider) sendOne(ctx context.Context, m Message) Result {
	var body bytes.Buffer
	if err := p.BodyTemplate.Execute(&body, TemplateData{Mobile: m.Mobile, Text: m.Text, Sender: p.Sender}); err != nil {
		return Result{MemberID: m.MemberID, OK: false, Err: "template: " + err.Error()}
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.URL, bytes.NewReader(body.Bytes()))
	if err != nil {
		return Result{MemberID: m.MemberID, OK: false, Err: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if p.AuthHeader != "" {
		if name, value, ok := strings.Cut(p.AuthHeader, ":"); ok {
			req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
		}
	}

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{MemberID: m.MemberID, OK: false, Err: err.Error()}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{MemberID: m.MemberID, OK: false, Err: resp.Status, Ref: string(respBody)}
	}
	return Result{MemberID: m.MemberID, OK: true, Ref: string(respBody)}
}

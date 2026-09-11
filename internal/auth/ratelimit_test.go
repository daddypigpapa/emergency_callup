package auth

import (
	"testing"
	"time"
)

// R7: same login ID failing 10 times -> increasing wait, reset on success, no lockout.
func TestLoginLimiter_BackoffIncreasesAndResetsOnSuccess(t *testing.T) {
	l := NewLoginLimiter()
	now := time.Now()

	for i := 0; i < 4; i++ {
		l.RecordFailure("k1234", now)
	}
	if w := l.LoginBackoff("k1234", now); w != 0 {
		t.Errorf("after 4 failures, backoff should still be 0, got %v", w)
	}

	l.RecordFailure("k1234", now) // 5th failure
	w := l.LoginBackoff("k1234", now)
	if w != 2*time.Second {
		t.Errorf("after 5 failures, backoff = %v, want 2s", w)
	}

	l.RecordFailure("k1234", now) // 6th
	w = l.LoginBackoff("k1234", now)
	if w != 4*time.Second {
		t.Errorf("after 6 failures, backoff = %v, want 4s", w)
	}

	// Never locked out entirely: waiting long enough always clears backoff.
	if w := l.LoginBackoff("k1234", now.Add(time.Hour)); w != 0 {
		t.Errorf("after waiting, backoff should clear, got %v", w)
	}

	// Cap at 60s even with many failures.
	for i := 0; i < 10; i++ {
		l.RecordFailure("k1234", now)
	}
	if w := l.LoginBackoff("k1234", now); w > 60*time.Second {
		t.Errorf("backoff must be capped at 60s, got %v", w)
	}

	l.RecordSuccess("k1234")
	if w := l.LoginBackoff("k1234", now); w != 0 {
		t.Errorf("after success, backoff should reset to 0, got %v", w)
	}
}

func TestLoginLimiter_DifferentLoginIDsIndependent(t *testing.T) {
	l := NewLoginLimiter()
	now := time.Now()
	for i := 0; i < 6; i++ {
		l.RecordFailure("victim", now)
	}
	if w := l.LoginBackoff("other", now); w != 0 {
		t.Errorf("unrelated login ID should not be throttled, got %v", w)
	}
}

func TestLoginLimiter_IPCap(t *testing.T) {
	l := NewLoginLimiter()
	now := time.Now()
	ip := "1.2.3.4"
	for i := 0; i < 60; i++ {
		if !l.AllowIP(ip, now) {
			t.Fatalf("attempt %d should be allowed within 60/min cap", i+1)
		}
	}
	if l.AllowIP(ip, now) {
		t.Error("61st attempt within the same minute should be denied")
	}
	if !l.AllowIP(ip, now.Add(61*time.Second)) {
		t.Error("attempt in a new window should be allowed")
	}
}

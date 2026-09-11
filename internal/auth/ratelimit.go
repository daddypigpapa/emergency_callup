package auth

import (
	"sync"
	"time"
)

// LoginLimiter implements SPEC §11.1's login throttling:
//   - per login ID: after 4 consecutive failures, each further attempt must
//     wait 2^(failures-4) seconds (capped at 60s) since the last failure.
//     No account lockout ("계정 잠금은 하지 않음" — deliberate, so a flood of
//     bad guesses against one ID cannot lock out the real person during an
//     emergency).
//   - per source IP: at most 60 attempts per rolling minute (loose, because
//     many field staff may share a carrier NAT IP).
//
// This is in-memory only (SPEC §2.1: single server, no external state
// store) and sized for hundreds of accounts, not attacker-scale cardinality.
type LoginLimiter struct {
	mu      sync.Mutex
	byLogin map[string]*loginState
	byIP    map[string]*ipState
}

type loginState struct {
	failures    int
	lastFailure time.Time
}

type ipState struct {
	windowStart time.Time
	count       int
}

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{
		byLogin: make(map[string]*loginState),
		byIP:    make(map[string]*ipState),
	}
}

// LoginBackoff returns how long the caller must still wait before this login
// attempt for loginID is allowed. Zero means "allowed now".
func (l *LoginLimiter) LoginBackoff(loginID string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.byLogin[loginID]
	if !ok || st.failures < 5 {
		return 0
	}
	wait := backoffFor(st.failures)
	elapsed := now.Sub(st.lastFailure)
	if elapsed >= wait {
		return 0
	}
	return wait - elapsed
}

// backoffFor returns 2^(failures-4) seconds capped at 60s, for failures>=5.
func backoffFor(failures int) time.Duration {
	shift := failures - 4
	if shift > 6 { // 2^6 = 64 already exceeds the 60s cap
		shift = 6
	}
	secs := 1 << shift // failures=5 -> 2^1=2s
	if secs > 60 {
		secs = 60
	}
	return time.Duration(secs) * time.Second
}

// RecordFailure registers one failed login attempt for loginID.
func (l *LoginLimiter) RecordFailure(loginID string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.byLogin[loginID]
	if !ok {
		st = &loginState{}
		l.byLogin[loginID] = st
	}
	st.failures++
	st.lastFailure = now
}

// RecordSuccess clears the failure counter for loginID ("성공 시 초기화").
func (l *LoginLimiter) RecordSuccess(loginID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byLogin, loginID)
}

// AllowIP reports whether another attempt from ip is allowed under the
// 60-per-minute cap, and records this attempt if so.
func (l *LoginLimiter) AllowIP(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.byIP[ip]
	if !ok || now.Sub(st.windowStart) >= time.Minute {
		l.byIP[ip] = &ipState{windowStart: now, count: 1}
		return true
	}
	if st.count >= 60 {
		return false
	}
	st.count++
	return true
}

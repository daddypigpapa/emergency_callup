// Package auth implements password hashing, session management and login
// rate limiting. See docs/SPEC.md §11.1.
package auth

import (
	"context"
	"errors"
	"runtime"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is fixed per SPEC §11.1 ("해시 | bcrypt cost 10").
const BcryptCost = 10

// hashWaitTimeout is how long a caller waits for a free hashing slot before
// giving up (SPEC §11.1: "대기 5초 넘으면 503 + retryAfter").
const hashWaitTimeout = 5 * time.Second

// ErrHashBusy is returned when no hashing slot became free within
// hashWaitTimeout. Callers (httpapi) must map this to HTTP 503 with a
// Retry-After hint.
var ErrHashBusy = errors.New("auth: password hashing queue busy")

// Hasher serializes bcrypt work to at most runtime.NumCPU() concurrent
// operations, preventing a burst of login attempts from starving the server
// (SPEC §2.2 M1 / testai regression: "동기 해시로 서버 정지").
type Hasher struct {
	sem chan struct{}
}

// NewHasher builds a Hasher sized to the number of available CPUs.
func NewHasher() *Hasher {
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	return &Hasher{sem: make(chan struct{}, n)}
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrHashBusy
	}
}

func (h *Hasher) release() { <-h.sem }

// Hash produces a bcrypt hash of password, waiting for a free slot.
func (h *Hasher) Hash(password string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hashWaitTimeout)
	defer cancel()
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	b, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Verify checks password against hash, waiting for a free slot. It returns
// (false, nil) for a normal mismatch, and a non-nil error only for busy
// queues or malformed hashes.
func (h *Hasher) Verify(hash, password string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hashWaitTimeout)
	defer cancel()
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
		return false, nil
	default:
		return false, err
	}
}

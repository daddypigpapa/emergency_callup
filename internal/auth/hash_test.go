package auth

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestHasher_HashAndVerify(t *testing.T) {
	h := NewHasher()
	hash, err := h.Hash("correcthorsebattery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, err := h.Verify(hash, "correcthorsebattery")
	if err != nil || !ok {
		t.Fatalf("verify correct password: ok=%v err=%v", ok, err)
	}
	ok, err = h.Verify(hash, "wrongpassword")
	if err != nil {
		t.Fatalf("verify wrong password returned error: %v", err)
	}
	if ok {
		t.Fatal("verify wrong password: expected false")
	}
}

// M1 / C4 regression: hashing must be capacity-limited, not spawn unbounded
// concurrent bcrypt work.
func TestHasher_ConcurrencyBounded(t *testing.T) {
	h := NewHasher()
	var inFlight, maxInFlight int
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := make(chan struct{})
			go func() {
				h.acquire(context.Background())
				mu.Lock()
				inFlight++
				if inFlight > maxInFlight {
					maxInFlight = inFlight
				}
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				inFlight--
				mu.Unlock()
				h.release()
				close(ctx)
			}()
			<-ctx
		}()
	}
	wg.Wait()

	if maxInFlight > cap(h.sem) {
		t.Errorf("max in-flight %d exceeded semaphore capacity %d", maxInFlight, cap(h.sem))
	}
}

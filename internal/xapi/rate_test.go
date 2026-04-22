package xapi

import (
	"sync"
	"testing"
	"time"
)

func TestRateLimiterDisabled(t *testing.T) {
	var nilLimiter *RateLimiter
	nilLimiter.Wait() // must not panic

	r := NewRateLimiter(0)
	slept := 0
	r.sleep = func(time.Duration) { slept++ }
	r.Wait()
	r.Wait()
	if slept != 0 {
		t.Fatalf("expected no sleeps when interval<=0, got %d", slept)
	}
}

func TestRateLimiterSpacesCalls(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	current := base
	nowFn := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	var slept []time.Duration
	sleepFn := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		slept = append(slept, d)
		current = current.Add(d)
	}

	r := NewRateLimiter(100 * time.Millisecond)
	r.now = nowFn
	r.sleep = sleepFn

	// First call: no wait, schedules next slot 100ms out.
	r.Wait()
	if len(slept) != 0 {
		t.Fatalf("first call should not sleep, got %v", slept)
	}

	// Second call happens immediately → must sleep ~100ms.
	r.Wait()
	if len(slept) != 1 || slept[0] != 100*time.Millisecond {
		t.Fatalf("expected one 100ms sleep, got %v", slept)
	}

	// Advance past the next slot — no sleep expected.
	mu.Lock()
	current = current.Add(500 * time.Millisecond)
	mu.Unlock()
	r.Wait()
	if len(slept) != 1 {
		t.Fatalf("expected still one sleep after advancing clock, got %v", slept)
	}
}

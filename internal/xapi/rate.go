package xapi

import (
	"sync"
	"time"
)

// RateLimiter serializes outgoing requests so that consecutive calls are
// spaced by at least Interval. Concurrent callers queue and each is released
// after the appropriate wait. A nil receiver or non-positive interval makes
// Wait a no-op, which keeps construction cheap and lets tests disable
// throttling.
type RateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time

	now   func() time.Time
	sleep func(time.Duration)
}

// NewRateLimiter returns a limiter that enforces at least interval between
// requests. When interval <= 0 the limiter is effectively disabled.
func NewRateLimiter(interval time.Duration) *RateLimiter {
	return &RateLimiter{
		interval: interval,
		now:      time.Now,
		sleep:    time.Sleep,
	}
}

// Wait blocks until the caller is allowed to perform the next request.
func (r *RateLimiter) Wait() {
	if r == nil || r.interval <= 0 {
		return
	}
	r.mu.Lock()
	now := r.now()
	var wait time.Duration
	if now.Before(r.next) {
		wait = r.next.Sub(now)
		r.next = r.next.Add(r.interval)
	} else {
		r.next = now.Add(r.interval)
	}
	r.mu.Unlock()
	if wait > 0 {
		r.sleep(wait)
	}
}

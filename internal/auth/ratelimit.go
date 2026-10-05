package auth

import (
	"sync"
	"time"
)

// Limiter is a fixed-window counter keyed by an arbitrary string (client IP).
type Limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	count int
	reset time.Time
}

// NewLimiter allows limit events per window per key.
func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{limit: limit, window: window, buckets: make(map[string]*bucket), now: time.Now}
}

// Allow records an event for key and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		b = &bucket{reset: now.Add(l.window)}
		l.buckets[key] = b
	}
	b.count++
	return b.count <= l.limit
}

// Sweep removes expired buckets.
func (l *Limiter) Sweep() {
	now := l.now()
	l.mu.Lock()
	for k, b := range l.buckets {
		if now.After(b.reset) {
			delete(l.buckets, k)
		}
	}
	l.mu.Unlock()
}

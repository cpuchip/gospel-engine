// Package ratelimit enforces api_tokens.rate_limit: a token bucket per key,
// in memory, holding up to rate_limit requests and refilling at rate_limit per
// minute. One Limiter is shared by the REST middleware and the /mcp mount, so
// a key has one budget however it arrives. State is per process: a restart
// refills every bucket, which errs toward the caller.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

// idleAfter is how long a bucket may go unused before a sweep drops it; by
// then it has refilled completely, so dropping it changes nothing.
const idleAfter = 10 * time.Minute

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter holds one bucket per token id. The zero value is not usable; call New.
type Limiter struct {
	mu        sync.Mutex
	buckets   map[int64]*bucket
	now       func() time.Time
	lastSweep time.Time
}

// New returns an empty Limiter on the wall clock.
func New() *Limiter {
	return &Limiter{buckets: map[int64]*bucket{}, now: time.Now}
}

// Allow spends one request from key id's bucket, whose capacity and refill
// are perMinute. It returns false and how long until a request would be
// allowed when the bucket is empty. perMinute <= 0 means 60, the column's
// default.
func (l *Limiter) Allow(id int64, perMinute int) (bool, time.Duration) {
	if perMinute <= 0 {
		perMinute = 60
	}
	capacity := float64(perMinute)
	perSecond := capacity / 60
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	b, ok := l.buckets[id]
	if !ok {
		b = &bucket{tokens: capacity, last: now}
		l.buckets[id] = b
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = math.Min(capacity, b.tokens+el*perSecond)
	}
	b.last = now
	if b.tokens > capacity { // an admin lowered the limit
		b.tokens = capacity
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / perSecond * float64(time.Second))
	return false, wait
}

// sweep drops idle buckets at most once a minute. Caller holds mu.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for id, b := range l.buckets {
		if now.Sub(b.last) > idleAfter {
			delete(l.buckets, id)
		}
	}
}

// RetryAfterSeconds renders a wait as a Retry-After value: whole seconds,
// rounded up, at least 1.
func RetryAfterSeconds(d time.Duration) int {
	s := int(math.Ceil(d.Seconds()))
	if s < 1 {
		s = 1
	}
	return s
}

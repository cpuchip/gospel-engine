package ratelimit

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newAt(c *clock) *Limiter {
	l := New()
	l.now = c.now
	return l
}

func TestBurstThenRefill(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newAt(c)
	for i := 0; i < 60; i++ {
		if ok, _ := l.Allow(1, 60); !ok {
			t.Fatalf("request %d of a 60/min burst refused", i+1)
		}
	}
	ok, wait := l.Allow(1, 60)
	if ok {
		t.Fatal("61st request in the same instant allowed")
	}
	if wait != time.Second {
		t.Errorf("wait = %s, want 1s at 60/min", wait)
	}
	c.advance(999 * time.Millisecond)
	if ok, _ := l.Allow(1, 60); ok {
		t.Error("allowed before a whole token refilled")
	}
	c.advance(2 * time.Millisecond)
	if ok, _ := l.Allow(1, 60); !ok {
		t.Error("refused after a token refilled")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newAt(c)
	for i := 0; i < 5; i++ {
		l.Allow(1, 5)
	}
	if ok, _ := l.Allow(1, 5); ok {
		t.Fatal("key 1 not limited")
	}
	if ok, _ := l.Allow(2, 5); !ok {
		t.Error("key 2 limited by key 1's use")
	}
}

func TestLimitChangesApply(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newAt(c)
	for i := 0; i < 10; i++ {
		l.Allow(1, 10)
	}
	if ok, _ := l.Allow(1, 10); ok {
		t.Fatal("not limited at 10/min")
	}
	// an admin raises the key to 600/min: refill is now 10 per second
	c.advance(100 * time.Millisecond)
	if ok, _ := l.Allow(1, 600); !ok {
		t.Error("raised limit not applied to refill")
	}
	// lowered to 2/min: the bucket is clamped to the new capacity
	c.advance(time.Hour)
	l.Allow(1, 2)
	l.Allow(1, 2)
	if ok, _ := l.Allow(1, 2); ok {
		t.Error("lowered limit not applied to capacity")
	}
}

func TestZeroLimitMeansDefault(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newAt(c)
	n := 0
	for n < 1000 { // bounded, so a limiter that never refuses fails instead of hanging
		if ok, _ := l.Allow(1, 0); !ok {
			break
		}
		n++
	}
	if n != 600 {
		t.Errorf("rate_limit 0 allowed %d, want the default 600", n)
	}
}

func TestSweepDropsOnlyIdle(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newAt(c)
	l.Allow(1, 60)
	c.advance(9 * time.Minute)
	l.Allow(2, 60)
	c.advance(2 * time.Minute) // key 1 idle 11 min, key 2 idle 2 min
	l.Allow(3, 60)
	if _, ok := l.buckets[1]; ok {
		t.Error("idle bucket kept")
	}
	if _, ok := l.buckets[2]; !ok {
		t.Error("recent bucket dropped")
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	for d, want := range map[time.Duration]int{0: 1, 300 * time.Millisecond: 1, time.Second: 1, 1001 * time.Millisecond: 2, 30 * time.Second: 30} {
		if got := RetryAfterSeconds(d); got != want {
			t.Errorf("RetryAfterSeconds(%s) = %d, want %d", d, got, want)
		}
	}
}

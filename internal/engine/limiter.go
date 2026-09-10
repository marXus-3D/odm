package engine

import (
	"context"
	"sync"
	"time"
)

// Limiter is a token bucket that caps aggregate throughput.
//
// One Limiter is shared by every segment of every download, so the ceiling
// applies to the whole application rather than per connection -- a per-socket
// cap multiplied by eight connections is not a cap at all.
//
// The zero value, and any Limiter whose rate is zero, is unlimited.
type Limiter struct {
	mu     sync.Mutex
	rate   float64 // bytes per second; 0 means unlimited
	burst  float64 // bucket capacity in bytes
	tokens float64
	last   time.Time
}

// NewLimiter returns a limiter capped at bps bytes per second. A bps of zero
// or less means no limit.
func NewLimiter(bps float64) *Limiter {
	l := &Limiter{}
	l.SetRate(bps)
	return l
}

// SetRate changes the ceiling at runtime. Existing waiters pick up the new
// rate on their next refill.
func (l *Limiter) SetRate(bps float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if bps < 0 {
		bps = 0
	}
	l.rate = bps
	// A quarter second of traffic is enough burst to keep large reads from
	// serializing, without letting the average drift above the ceiling.
	l.burst = bps / 4
	if l.burst < float64(DefaultChunkSize) {
		l.burst = float64(DefaultChunkSize)
	}
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = time.Now()
}

// Rate returns the current ceiling in bytes per second, 0 when unlimited.
func (l *Limiter) Rate() float64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rate
}

// Wait blocks until n bytes may be consumed, or ctx is done.
func (l *Limiter) Wait(ctx context.Context, n int) error {
	if l == nil || n <= 0 {
		return nil
	}
	for {
		l.mu.Lock()
		if l.rate <= 0 {
			l.mu.Unlock()
			return nil // unlimited
		}
		now := time.Now()
		if l.last.IsZero() {
			l.last = now
		}
		l.tokens += now.Sub(l.last).Seconds() * l.rate
		l.last = now
		if l.tokens > l.burst {
			l.tokens = l.burst
		}

		need := float64(n)
		// A read larger than the whole bucket would never be satisfiable, so
		// let it through once the bucket is full rather than deadlocking.
		if need > l.burst {
			need = l.burst
		}
		if l.tokens >= need {
			l.tokens -= need
			l.mu.Unlock()
			return nil
		}
		deficit := need - l.tokens
		wait := time.Duration(deficit / l.rate * float64(time.Second))
		l.mu.Unlock()

		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

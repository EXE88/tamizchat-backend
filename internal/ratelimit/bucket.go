// Package ratelimit provides a small token bucket, used to keep one client
// from flooding the server or the other users.
package ratelimit

import (
	"sync"
	"time"
)

// Bucket is a token bucket that refills continuously.
//
// The limits are passed to Allow rather than stored, so an admin changing the
// configured rate takes effect immediately, on the next message, without
// rebuilding anyone's bucket.
type Bucket struct {
	mu      sync.Mutex
	tokens  float64
	last    time.Time
	started bool
}

// Allow consumes one token if the bucket has one. capacity is the burst size —
// how many messages may be sent back to back after a quiet period — and
// perSecond is the sustained refill rate.
func (b *Bucket) Allow(capacity int, perSecond float64) bool {
	return b.allowAt(time.Now(), capacity, perSecond)
}

func (b *Bucket) allowAt(now time.Time, capacity int, perSecond float64) bool {
	if capacity < 1 {
		capacity = 1
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.started {
		// A fresh client starts with a full burst.
		b.tokens = float64(capacity)
		b.last = now
		b.started = true
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * perSecond
			b.last = now
		}
		if b.tokens > float64(capacity) {
			b.tokens = float64(capacity)
		}
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

package httpapi

import (
	"hash/fnv"
	"math"
	"sync"
	"time"
)

// Default per-user token-bucket limits for invoice creation. A single account has
// no legitimate reason to open invoices faster than this; the cap blunts one
// abuser spamming the crypto gateways without throttling everyone else (unlike a
// single global bucket). NOTE: keyed by anon_user_id, so it is defense-in-depth,
// not a complete control — the endpoint still needs auth + a horizontal-scale
// ceiling (Redis) per docs/wave-2/TZ-hardening.md.
const (
	defaultInvoiceBurst      = 10 // per-user bucket capacity (max burst)
	defaultInvoiceRefillRate = 1  // per-user tokens refilled per second
	limiterStripes           = 256
)

// stripedLimiter is a bounded set of token buckets keyed by hashing the caller id
// into a fixed stripe pool. Fixed size avoids an unbounded per-id map (memory leak
// keyed by the user population); rare cross-user collisions only share a bucket.
type stripedLimiter struct {
	buckets [limiterStripes]*tokenBucket
}

func newStripedLimiter(burst, refillPerSec float64, now func() time.Time) *stripedLimiter {
	s := &stripedLimiter{}
	for i := range s.buckets {
		s.buckets[i] = newTokenBucket(burst, refillPerSec, now)
	}
	return s
}

// allow reports whether the given key is within its rate limit.
func (s *stripedLimiter) allow(key string) bool {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return s.buckets[h.Sum32()%limiterStripes].allow()
}

// tokenBucket is a small, concurrency-safe token-bucket rate limiter. Time is
// injectable so the refill behaviour is deterministic in tests.
type tokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	capacity   float64
	refillRate float64 // tokens added per second
	last       time.Time
	now        func() time.Time
}

// newTokenBucket builds a full bucket of the given capacity that refills at
// refillPerSec tokens/second.
func newTokenBucket(capacity, refillPerSec float64, now func() time.Time) *tokenBucket {
	if now == nil {
		now = time.Now
	}
	return &tokenBucket{
		tokens:     capacity,
		capacity:   capacity,
		refillRate: refillPerSec,
		last:       now(),
		now:        now,
	}
}

// allow consumes one token, refilling first based on elapsed time. It reports
// whether the request is within the rate limit.
func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(b.capacity, b.tokens+elapsed*b.refillRate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

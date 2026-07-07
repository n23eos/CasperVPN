package ingest

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// RateLimiter is a token-bucket limiter with a per-source bucket plus a global
// ceiling. Source keys are a SALTED HMAC of the client's coarse origin, held only
// in memory with TTL eviction — never persisted and never linked to a stored
// signal. The salt is process-random, so buckets cannot be correlated across
// restarts or back to an IP. This keeps rate-limiting compatible with anonymity.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	global  *bucket
	salt    []byte

	ratePerSec  float64
	burst       float64
	globalRate  float64
	globalBurst float64
	ttl         time.Duration
	now         func() time.Time
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// NewRateLimiter builds a limiter. globalRate/globalBurst bound total intake;
// ratePerSec/burst bound a single source. now is injectable for tests.
func NewRateLimiter(ratePerSec, burst, globalRate, globalBurst int, now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	salt := make([]byte, 32)
	_, _ = rand.Read(salt) // process-random; failure only weakens unlinkability, not correctness
	return &RateLimiter{
		buckets:     map[string]*bucket{},
		global:      &bucket{tokens: float64(globalBurst), lastSeen: now()},
		salt:        salt,
		ratePerSec:  float64(ratePerSec),
		burst:       float64(burst),
		globalRate:  float64(globalRate),
		globalBurst: float64(globalBurst),
		ttl:         10 * time.Minute,
		now:         now,
	}
}

// SourceToken hashes a raw origin (e.g. RemoteAddr) into an opaque, salted bucket
// key. The raw value is never stored; only this token lives in memory, briefly.
func (r *RateLimiter) SourceToken(raw string) string {
	mac := hmac.New(sha256.New, r.salt)
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

// Allow charges n tokens against both the source bucket and the global bucket.
// Returns false (rate limited) if either is exhausted.
func (r *RateLimiter) Allow(sourceToken string, n int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	cost := float64(n)

	// Global ceiling first.
	refill(r.global, r.globalRate, r.globalBurst, now)
	if r.global.tokens < cost {
		return false
	}

	b := r.buckets[sourceToken]
	if b == nil {
		b = &bucket{tokens: r.burst, lastSeen: now}
		r.buckets[sourceToken] = b
	}
	refill(b, r.ratePerSec, r.burst, now)
	if b.tokens < cost {
		return false
	}

	b.tokens -= cost
	b.lastSeen = now
	r.global.tokens -= cost
	r.evictLocked(now)
	return true
}

func refill(b *bucket, rate, max float64, now time.Time) {
	elapsed := now.Sub(b.lastSeen).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * rate
		if b.tokens > max {
			b.tokens = max
		}
		b.lastSeen = now
	}
}

// evictLocked drops idle buckets so the map does not grow unbounded (memory-leak
// guard for a long-running process). Caller holds the lock.
func (r *RateLimiter) evictLocked(now time.Time) {
	for k, b := range r.buckets {
		if now.Sub(b.lastSeen) > r.ttl {
			delete(r.buckets, k)
		}
	}
}

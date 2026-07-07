package telegram

import (
	"sync"
	"time"
)

// limiter is a per-user token bucket plus an antispam de-dup, protecting the bot
// from a flood (one abusive user) and from repeated identical commands. Keys are
// the messenger user id — already coarse and non-PII in this context — held only
// in memory with TTL eviction so the map cannot grow unbounded (memory-leak guard
// for a long-running process).
type limiter struct {
	mu      sync.Mutex
	buckets map[int64]*userState

	ratePerSec float64
	burst      float64
	cooldown   time.Duration // min gap between two IDENTICAL commands from a user
	ttl        time.Duration
	now        func() time.Time
}

type userState struct {
	tokens   float64
	lastSeen time.Time
	lastCmd  string
	lastCmdT time.Time
}

// newLimiter builds a limiter. now is injectable for deterministic tests.
func newLimiter(ratePerSec, burst int, cooldown time.Duration, now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	return &limiter{
		buckets:    map[int64]*userState{},
		ratePerSec: float64(ratePerSec),
		burst:      float64(burst),
		cooldown:   cooldown,
		ttl:        30 * time.Minute,
		now:        now,
	}
}

// allow reports whether a command from userID with text cmd may be served now.
// It rejects when the token bucket is empty (rate limit) OR the same command was
// served within the cooldown (antispam de-dup).
func (l *limiter) allow(userID int64, cmd string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	s := l.buckets[userID]
	if s == nil {
		s = &userState{tokens: l.burst, lastSeen: now}
		l.buckets[userID] = s
	}

	// Refill.
	if elapsed := now.Sub(s.lastSeen).Seconds(); elapsed > 0 {
		s.tokens += elapsed * l.ratePerSec
		if s.tokens > l.burst {
			s.tokens = l.burst
		}
		s.lastSeen = now
	}

	// Antispam: identical command within cooldown is dropped without spending a
	// token (so a stuck client cannot drain the bucket AND cannot spam replies).
	if cmd != "" && cmd == s.lastCmd && now.Sub(s.lastCmdT) < l.cooldown {
		l.evictLocked(now)
		return false
	}

	if s.tokens < 1 {
		l.evictLocked(now)
		return false
	}
	s.tokens--
	s.lastCmd = cmd
	s.lastCmdT = now
	l.evictLocked(now)
	return true
}

// evictLocked drops idle user states. Caller holds the lock.
func (l *limiter) evictLocked(now time.Time) {
	for k, s := range l.buckets {
		if now.Sub(s.lastSeen) > l.ttl {
			delete(l.buckets, k)
		}
	}
}

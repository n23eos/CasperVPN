// Package cache stores rendered subscription payloads with a TTL and supports
// invalidation on control-plane signals (a node going blocked triggers a
// rebuild). Combined with the Profile-Update-Interval header, this washes
// blocked nodes out of clients quickly.
package cache

import (
	"sync"
	"time"
)

// Entry is a cached rendered payload plus the response headers derived at render
// time (so a cache hit still emits correct Subscription-Userinfo etc.).
type Entry struct {
	Body        []byte
	ContentType string
	Headers     map[string]string
}

type stored struct {
	Entry
	version uint64
	expires time.Time
}

// Cache is a concurrency-safe payload cache keyed by token+format. A global
// version counter is bumped on broad invalidation (e.g. a node blocked), which
// logically evicts every entry at once without walking the map.
type Cache struct {
	mu      sync.Mutex
	ttl     time.Duration
	version uint64
	entries map[string]stored
	now     func() time.Time
}

// New builds a cache with the given TTL. now may be nil (defaults to time.Now).
func New(ttl time.Duration, now func() time.Time) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{ttl: ttl, entries: map[string]stored{}, now: now}
}

func key(token, format string) string { return token + "|" + format }

// Get returns a fresh entry for token+format, or ok=false on miss/stale/expired.
func (c *Cache) Get(token, format string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.entries[key(token, format)]
	if !ok || s.version != c.version || !s.expires.After(c.now()) {
		return Entry{}, false
	}
	return s.Entry, true
}

// Put stores a rendered payload for token+format under the current version.
func (c *Cache) Put(token, format string, e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key(token, format)] = stored{
		Entry:   e,
		version: c.version,
		expires: c.now().Add(c.ttl),
	}
}

// InvalidateAll bumps the version, logically evicting every entry. Called when a
// node is blocked so every subscription is rebuilt without the blocked node.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.version++
}

// InvalidateToken drops all cached formats for one token (leaked-link / rotate).
func (c *Cache) InvalidateToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range []string{"base64", "singbox", "clash"} {
		delete(c.entries, key(token, f))
	}
}

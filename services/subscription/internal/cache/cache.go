// Package cache stores rendered subscription payloads with a TTL and supports
// invalidation on control-plane signals (a node going blocked triggers a
// rebuild). Combined with the Profile-Update-Interval header, this washes
// blocked nodes out of clients quickly.
package cache

import (
	"strings"
	"sync"
	"time"
)

// maxEntries bounds the map: keys are user tokens × formats, so an unbounded
// cache grows linearly with every token that ever hit the service — an OOM on
// live traffic. When full, Put first purges dead entries (expired or logically
// evicted by a version bump), then falls back to evicting arbitrary ones.
const maxEntries = 100_000

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
	k := key(token, format)
	s, ok := c.entries[k]
	if !ok {
		return Entry{}, false
	}
	if s.version != c.version || !s.expires.After(c.now()) {
		delete(c.entries, k) // dead entry — reclaim instead of leaving it forever
		return Entry{}, false
	}
	return s.Entry, true
}

// Put stores a rendered payload for token+format under the current version,
// evicting when the cache is at capacity.
func (c *Cache) Put(token, format string, e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(token, format)
	if _, exists := c.entries[k]; !exists && len(c.entries) >= maxEntries {
		c.evictLocked()
	}
	c.entries[k] = stored{
		Entry:   e,
		version: c.version,
		expires: c.now().Add(c.ttl),
	}
}

// evictLocked frees room: dead entries first (expired / stale version), then —
// if the cache is genuinely full of live entries — arbitrary ones. Evicting a
// live cache entry only costs a re-render on the next hit.
func (c *Cache) evictLocked() {
	now := c.now()
	for k, s := range c.entries {
		if s.version != c.version || !s.expires.After(now) {
			delete(c.entries, k)
		}
	}
	for k := range c.entries {
		if len(c.entries) < maxEntries {
			break
		}
		delete(c.entries, k)
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
// It scans by key prefix rather than a hardcoded format list: a newly added
// render format must never keep serving a revoked token from cache.
func (c *Cache) InvalidateToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := token + "|"
	for k := range c.entries {
		if strings.HasPrefix(k, prefix) {
			delete(c.entries, k)
		}
	}
}

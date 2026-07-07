package ingest

import (
	"sync"
	"time"
)

// Deduper rejects repeat SignalIDs within a TTL. SignalID is a client-generated
// random key; replaying it is a cheap amplification attack (one observation posted
// thousands of times to fake source volume). Collapsing duplicates here means the
// aggregator sees each observation once. The TTL bounds memory; it should be ≥ the
// aggregation window so replays within a live verdict window are always caught.
type Deduper struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
	now  func() time.Time
}

// NewDeduper builds a deduper with the given TTL. now is injectable for tests.
func NewDeduper(ttl time.Duration, now func() time.Time) *Deduper {
	if now == nil {
		now = time.Now
	}
	return &Deduper{seen: map[string]time.Time{}, ttl: ttl, now: now}
}

// Fresh reports whether id has NOT been seen within the TTL, recording it as seen.
// Returns false for a duplicate (should be dropped). Empty ids are never fresh.
func (d *Deduper) Fresh(id string) bool {
	if id == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if ts, ok := d.seen[id]; ok && now.Sub(ts) < d.ttl {
		return false
	}
	d.seen[id] = now
	d.sweepLocked(now)
	return true
}

// sweepLocked evicts expired entries opportunistically. Caller holds the lock.
func (d *Deduper) sweepLocked(now time.Time) {
	// Bound the work: only sweep when the map grows past a threshold.
	if len(d.seen) < 4096 {
		return
	}
	for k, ts := range d.seen {
		if now.Sub(ts) >= d.ttl {
			delete(d.seen, k)
		}
	}
}

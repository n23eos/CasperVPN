package channel

import (
	"context"
	"sort"
	"sync"
)

// entry pairs a channel with its configured priority (lower = preferred).
type entry struct {
	ch       Channel
	priority int
}

// Registry is the DYNAMIC, updatable set of channels. Channels are added from
// config at boot and can be added/removed at runtime (e.g. an operator retires a
// burned git-raw mirror and adds a fresh one) — the list is never hardcoded.
type Registry struct {
	mu      sync.RWMutex
	entries []entry
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Add registers a channel at a priority. Re-adding the same Kind replaces it, so
// config reloads are idempotent.
func (r *Registry) Add(ch Channel, priority int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.entries {
		if r.entries[i].ch.Kind() == ch.Kind() {
			r.entries[i] = entry{ch: ch, priority: priority}
			r.sortLocked()
			return
		}
	}
	r.entries = append(r.entries, entry{ch: ch, priority: priority})
	r.sortLocked()
}

// Remove drops a channel by Kind (e.g. retire a blocked channel). No-op if absent.
func (r *Registry) Remove(kind Kind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.entries[:0]
	for _, e := range r.entries {
		if e.ch.Kind() != kind {
			out = append(out, e)
		}
	}
	r.entries = out
}

// Ordered returns the channels in priority order (a copy, safe to iterate).
func (r *Registry) Ordered() []Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Channel, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.ch
	}
	return out
}

// Get returns the channel of a given Kind, if registered.
func (r *Registry) Get(kind Kind) (Channel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		if e.ch.Kind() == kind {
			return e.ch, true
		}
	}
	return nil, false
}

// Kinds returns the registered channel kinds in priority order.
func (r *Registry) Kinds() []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Kind, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.ch.Kind()
	}
	return out
}

// Health probes every channel and reports kind -> healthy.
func (r *Registry) Health(ctx context.Context) map[Kind]bool {
	chans := r.Ordered()
	out := make(map[Kind]bool, len(chans))
	for _, c := range chans {
		out[c.Kind()] = c.Healthy(ctx)
	}
	return out
}

// sortLocked keeps entries in ascending priority (stable so equal priorities keep
// insertion order). Caller holds the write lock.
func (r *Registry) sortLocked() {
	sort.SliceStable(r.entries, func(i, j int) bool {
		return r.entries[i].priority < r.entries[j].priority
	})
}

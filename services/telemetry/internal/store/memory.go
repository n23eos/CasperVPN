package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/caspervpn/contracts"
)

// MemoryStore is an in-memory, mutation-safe time-series store. It is the default
// backend: fully functional and the one exercised by tests. Retention is enforced
// by Prune (called on a timer) plus a hard cap that drops the oldest points.
//
// Concurrency: all access is guarded by a single RWMutex. Reads return copies so
// callers never alias internal slices (immutability at the boundary).
type MemoryStore struct {
	mu      sync.RWMutex
	signals []contracts.FieldSignal
	health  []contracts.HealthEvent
	maxLen  int // hard cap per series; 0 = unbounded
}

// NewMemoryStore builds a memory store. maxLen caps each series to bound memory in
// long-running processes (CLAUDE.md production checklist); 0 means unbounded.
func NewMemoryStore(maxLen int) *MemoryStore {
	return &MemoryStore{maxLen: maxLen}
}

// WriteSignals appends signals, keeping the series sorted by ObservedAt and within
// the hard cap.
func (m *MemoryStore) WriteSignals(_ context.Context, sigs []contracts.FieldSignal) error {
	if len(sigs) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signals = append(m.signals, sigs...)
	sort.Slice(m.signals, func(i, j int) bool {
		return m.signals[i].ObservedAt.Before(m.signals[j].ObservedAt)
	})
	if m.maxLen > 0 && len(m.signals) > m.maxLen {
		// Drop the oldest overflow. New backing array = no aliasing of the trimmed head.
		m.signals = append([]contracts.FieldSignal(nil), m.signals[len(m.signals)-m.maxLen:]...)
	}
	return nil
}

// WriteHealth appends health events, kept sorted and capped.
func (m *MemoryStore) WriteHealth(_ context.Context, events []contracts.HealthEvent) error {
	if len(events) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.health = append(m.health, events...)
	sort.Slice(m.health, func(i, j int) bool {
		return m.health[i].ObservedAt.Before(m.health[j].ObservedAt)
	})
	if m.maxLen > 0 && len(m.health) > m.maxLen {
		m.health = append([]contracts.HealthEvent(nil), m.health[len(m.health)-m.maxLen:]...)
	}
	return nil
}

// SignalsSince returns a copy of signals observed at/after t.
func (m *MemoryStore) SignalsSince(_ context.Context, t time.Time) ([]contracts.FieldSignal, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]contracts.FieldSignal, 0, len(m.signals))
	for _, s := range m.signals {
		if !s.ObservedAt.Before(t) {
			out = append(out, s)
		}
	}
	return out, nil
}

// HealthSince returns a copy of health events observed at/after t.
func (m *MemoryStore) HealthSince(_ context.Context, t time.Time) ([]contracts.HealthEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]contracts.HealthEvent, 0, len(m.health))
	for _, e := range m.health {
		if !e.ObservedAt.Before(t) {
			out = append(out, e)
		}
	}
	return out, nil
}

// Prune removes points observed strictly before t and returns the count removed.
func (m *MemoryStore) Prune(_ context.Context, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0

	keptSig := make([]contracts.FieldSignal, 0, len(m.signals))
	for _, s := range m.signals {
		if s.ObservedAt.Before(before) {
			removed++
			continue
		}
		keptSig = append(keptSig, s)
	}
	m.signals = keptSig

	keptHealth := make([]contracts.HealthEvent, 0, len(m.health))
	for _, e := range m.health {
		if e.ObservedAt.Before(before) {
			removed++
			continue
		}
		keptHealth = append(keptHealth, e)
	}
	m.health = keptHealth

	return removed, nil
}

// Close is a no-op for the in-memory store.
func (m *MemoryStore) Close() error { return nil }

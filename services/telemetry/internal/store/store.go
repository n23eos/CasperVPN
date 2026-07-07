// Package store persists anonymous telemetry as time-ordered points and supports
// window queries plus retention pruning. It is deliberately narrow: it stores only
// what contracts.FieldSignal / contracts.HealthEvent carry — nothing that ties a
// signal to a user or an IP (anonymity by construction, CLAUDE.md [ANTI-BLOCK]).
package store

import (
	"context"
	"time"

	"github.com/caspervpn/contracts"
)

// Store is the persistence port. Implementations: MemoryStore (default, tested),
// PostgresStore (prod). Business logic depends on this interface, not the backend.
type Store interface {
	// WriteSignals appends already-sanitized, anonymous field signals.
	WriteSignals(ctx context.Context, sigs []contracts.FieldSignal) error
	// WriteHealth appends authoritative orchestrator health events.
	WriteHealth(ctx context.Context, events []contracts.HealthEvent) error

	// SignalsSince returns signals observed at/after t, oldest first.
	SignalsSince(ctx context.Context, t time.Time) ([]contracts.FieldSignal, error)
	// HealthSince returns health events observed at/after t, oldest first.
	HealthSince(ctx context.Context, t time.Time) ([]contracts.HealthEvent, error)

	// Prune deletes points observed strictly before t and returns how many were
	// removed. Drives retention.
	Prune(ctx context.Context, before time.Time) (int, error)

	// Close releases backend resources. No-op for the in-memory store.
	Close() error
}

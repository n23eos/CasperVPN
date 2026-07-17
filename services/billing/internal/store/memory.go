package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/contracts"
)

// memSettlement mirrors the Postgres settlements row: a claim latch plus the two
// recovery markers (activation applied, reconcile lease).
type memSettlement struct {
	claimedAt   time.Time
	activatedAt time.Time // zero = not yet activated
	leasedUntil time.Time // zero = not leased
}

// Memory is an in-memory Repository. Safe for concurrent use. All state is lost
// on restart — fine for the MVP/tests, not for production (see docs/billing.md).
type Memory struct {
	mu        sync.Mutex
	invoices  map[string]model.Invoice
	events    map[string]struct{}       // "provider|externalID" processed to completion
	settled   map[string]*memSettlement // invoiceID currently claimed/credited
	schedules map[string]model.Schedule
	now       func() time.Time
}

// NewMemory builds an empty in-memory store with the wall clock.
func NewMemory() *Memory {
	return NewMemoryWithClock(time.Now)
}

// NewMemoryWithClock builds an empty in-memory store with an injectable clock, so
// settlement recovery timing (claimed_at / lease expiry) is deterministic in tests.
func NewMemoryWithClock(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{
		invoices:  make(map[string]model.Invoice),
		events:    make(map[string]struct{}),
		settled:   make(map[string]*memSettlement),
		schedules: make(map[string]model.Schedule),
		now:       now,
	}
}

func (m *Memory) CreateInvoice(_ context.Context, inv model.Invoice) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invoices[inv.ID] = inv
	return nil
}

func (m *Memory) GetInvoice(_ context.Context, id string) (model.Invoice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invoices[id]
	if !ok {
		return model.Invoice{}, ErrNotFound
	}
	return inv, nil
}

func (m *Memory) SetInvoiceStatus(_ context.Context, id string, s model.Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invoices[id]
	if !ok {
		return ErrNotFound
	}
	inv.Status = s
	m.invoices[id] = inv
	return nil
}

func (m *Memory) OpenInvoices(_ context.Context) ([]model.Invoice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.Invoice
	for _, inv := range m.invoices {
		if inv.Status == model.StatusPending {
			out = append(out, inv)
		}
	}
	return out, nil
}

func (m *Memory) SeenEvent(_ context.Context, provider, externalID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, seen := m.events[provider+"|"+externalID]
	return seen, nil
}

func (m *Memory) RecordEvent(_ context.Context, provider, externalID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[provider+"|"+externalID] = struct{}{}
	return nil
}

func (m *Memory) ClaimSettlement(_ context.Context, invoiceID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, claimed := m.settled[invoiceID]; claimed {
		return false, nil
	}
	m.settled[invoiceID] = &memSettlement{claimedAt: m.now()}
	return true, nil
}

func (m *Memory) ReleaseSettlement(_ context.Context, invoiceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.settled, invoiceID)
	return nil
}

func (m *Memory) MarkSettlementActivated(_ context.Context, invoiceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.settled[invoiceID]; s != nil && s.activatedAt.IsZero() {
		s.activatedAt = m.now()
	}
	return nil
}

func (m *Memory) LeaseStuckSettlements(_ context.Context, olderThan time.Time, leaseFor time.Duration, limit int) ([]StuckSettlement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()

	// Collect eligible invoice ids, then lease deterministically (oldest claim first)
	// so a limit picks a stable set — mirrors Postgres ORDER BY claimed_at.
	type cand struct {
		id string
		s  *memSettlement
	}
	var cands []cand
	for id, s := range m.settled {
		inv, ok := m.invoices[id]
		if !ok || inv.Status != model.StatusPending {
			continue
		}
		if s.claimedAt.After(olderThan) {
			continue // younger than the recovery threshold — a live settle may own it
		}
		if !s.leasedUntil.IsZero() && s.leasedUntil.After(now) {
			continue // already leased by another reconciler
		}
		cands = append(cands, cand{id, s})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].s.claimedAt.Before(cands[j].s.claimedAt) })

	var out []StuckSettlement
	for _, c := range cands {
		if limit > 0 && len(out) >= limit {
			break
		}
		c.s.leasedUntil = now.Add(leaseFor)
		out = append(out, StuckSettlement{InvoiceID: c.id, Activated: !c.s.activatedAt.IsZero()})
	}
	return out, nil
}

func (m *Memory) ExpireOverdue(_ context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, inv := range m.invoices {
		if inv.Status != model.StatusPending {
			continue
		}
		if inv.ExpiresAt.IsZero() || !now.After(inv.ExpiresAt) {
			continue
		}
		if _, claimed := m.settled[id]; claimed {
			continue // never bury an invoice that is being credited
		}
		inv.Status = model.StatusExpired
		m.invoices[id] = inv
	}
	return nil
}

func (m *Memory) UpsertSchedule(_ context.Context, s model.Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedules[s.SubID] = s
	return nil
}

func (m *Memory) GetSchedule(_ context.Context, subID string) (model.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.schedules[subID]
	if !ok {
		return model.Schedule{}, ErrNotFound
	}
	return s, nil
}

// DueSchedules returns non-expired schedules whose expiry time has passed — the
// sweeper decides whether that means past_due or fully expired.
func (m *Memory) DueSchedules(_ context.Context, now time.Time) ([]model.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.Schedule
	for _, s := range m.schedules {
		if s.Status == string(contracts.SubscriptionStatusExpired) {
			continue
		}
		if !now.Before(s.ExpiresAt) {
			out = append(out, s)
		}
	}
	return out, nil
}

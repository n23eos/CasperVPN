package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/caspervpn/billing/internal/idgen"
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

	userLocksMu sync.Mutex
	userLocks   map[string]*sync.Mutex // per-user lock (process-local; NOT cross-instance)

	negativeCheck map[string]time.Time // invoiceID → last definitive negative on-chain check
	pollLeases    map[string]*memLease // invoiceID → durable poll lease
}

// memLease mirrors a poll_leases row: an opaque token, an observability owner, and
// the lease expiry.
type memLease struct {
	token string
	owner string
	until time.Time
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
		invoices:      make(map[string]model.Invoice),
		events:        make(map[string]struct{}),
		settled:       make(map[string]*memSettlement),
		schedules:     make(map[string]model.Schedule),
		now:           now,
		userLocks:     make(map[string]*sync.Mutex),
		negativeCheck: make(map[string]time.Time),
		pollLeases:    make(map[string]*memLease),
	}
}

// WithUserLock serializes fn per user with a process-local mutex. This is NOT
// cross-instance safe (a second billing process has its own map) — the Postgres
// store provides the real cross-instance guarantee; memory is for single-process
// dev/tests only. The userLocks map keeps one tiny mutex per distinct user and is
// never pruned; that unbounded growth is acceptable only because this store is
// dev/test-only (production uses Postgres).
func (m *Memory) WithUserLock(ctx context.Context, userID string, fn func(ctx context.Context) error) error {
	m.userLocksMu.Lock()
	mu, ok := m.userLocks[userID]
	if !ok {
		mu = &sync.Mutex{}
		m.userLocks[userID] = mu
	}
	m.userLocksMu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	return fn(ctx)
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

func (m *Memory) ExpireOverdue(_ context.Context, now time.Time, onchainProviders []string, grace time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	onchain := make(map[string]bool, len(onchainProviders))
	for _, p := range onchainProviders {
		onchain[p] = true
	}
	for id, inv := range m.invoices {
		if inv.Status != model.StatusPending || inv.ExpiresAt.IsZero() {
			continue
		}
		if _, claimed := m.settled[id]; claimed {
			continue // never bury an invoice being credited
		}
		if l, ok := m.pollLeases[id]; ok && l.until.After(now) {
			continue // being polled/handed off
		}
		if onchain[inv.Provider] {
			deadline := inv.ExpiresAt.Add(grace)
			if !now.After(deadline) { // strict: expire only when now > deadline
				continue
			}
			nc, ok := m.negativeCheck[id]
			if !ok || nc.Before(deadline) { // need a negative check taken at/after the deadline
				continue
			}
		} else if !now.After(inv.ExpiresAt) { // strict: now > expires_at
			continue
		}
		inv.Status = model.StatusExpired
		m.invoices[id] = inv
	}
	return nil
}

// AcquirePollLease takes the invoice's poll lease, reclaiming a lapsed lease. Returns
// a fresh token on success, or acquired=false if a live lease is held.
func (m *Memory) AcquirePollLease(_ context.Context, invoiceID, owner string, leaseFor time.Duration) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if l, ok := m.pollLeases[invoiceID]; ok && l.until.After(now) {
		return "", false, nil
	}
	token := idgen.New()
	m.pollLeases[invoiceID] = &memLease{token: token, owner: owner, until: now.Add(leaseFor)}
	return token, true, nil
}

// RenewPollLease extends the lease only if token still owns it.
func (m *Memory) RenewPollLease(_ context.Context, invoiceID, token string, leaseFor time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.pollLeases[invoiceID]
	if !ok || l.token != token {
		return false, nil
	}
	l.until = m.now().Add(leaseFor)
	return true, nil
}

// ReleasePollLease drops the lease only if token still owns it.
func (m *Memory) ReleasePollLease(_ context.Context, invoiceID, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.pollLeases[invoiceID]; ok && l.token == token {
		delete(m.pollLeases, invoiceID)
	}
	return nil
}

// RecordNegativeCheck stamps the last definitive negative on-chain check.
func (m *Memory) RecordNegativeCheck(_ context.Context, invoiceID string, checkAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inv, ok := m.invoices[invoiceID]; ok && inv.Status == model.StatusPending {
		m.negativeCheck[invoiceID] = checkAt
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

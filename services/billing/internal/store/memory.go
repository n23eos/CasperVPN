package store

import (
	"context"
	"sync"
	"time"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/contracts"
)

// Memory is an in-memory Repository. Safe for concurrent use. All state is lost
// on restart — fine for the MVP/tests, not for production (see docs/billing.md).
type Memory struct {
	mu        sync.Mutex
	invoices  map[string]model.Invoice
	events    map[string]struct{} // "provider|externalID" processed to completion
	settled   map[string]struct{} // invoiceID currently claimed/credited
	schedules map[string]model.Schedule
}

// NewMemory builds an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		invoices:  make(map[string]model.Invoice),
		events:    make(map[string]struct{}),
		settled:   make(map[string]struct{}),
		schedules: make(map[string]model.Schedule),
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
	m.settled[invoiceID] = struct{}{}
	return true, nil
}

func (m *Memory) ReleaseSettlement(_ context.Context, invoiceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.settled, invoiceID)
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

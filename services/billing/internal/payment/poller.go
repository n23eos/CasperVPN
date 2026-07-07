package payment

import (
	"context"
	"time"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/store"
)

// Poller drives poll-based gateways (e.g. direct on-chain watching). It reads the
// open invoices, asks every gateway for settlement events, and funnels them
// through the same Processor as webhooks — so idempotency holds regardless of
// whether a payment arrives by webhook or by poll.
type Poller struct {
	store     store.Repository
	registry  *Registry
	processor *Processor
	now       func() time.Time
}

// NewPoller wires the poller. now is injectable for deterministic tests.
func NewPoller(repo store.Repository, registry *Registry, processor *Processor, now func() time.Time) *Poller {
	if now == nil {
		now = time.Now
	}
	return &Poller{store: repo, registry: registry, processor: processor, now: now}
}

// RunOnce performs a single poll cycle. It first sweeps pending invoices whose
// window has elapsed into expired, then polls only the still-active ones. Errors
// on individual events are swallowed (logged by the caller) so one bad invoice
// cannot stall the rest.
func (p *Poller) RunOnce(ctx context.Context) error {
	open, err := p.store.OpenInvoices(ctx)
	if err != nil {
		return err
	}
	active := p.sweepExpired(ctx, open)
	for _, g := range p.registry.Gateways() {
		events, err := g.Poll(ctx, active)
		if err != nil {
			continue
		}
		for _, ev := range events {
			_ = p.processor.Process(ctx, ev)
		}
	}
	return nil
}

// sweepExpired transitions pending invoices past their ExpiresAt to expired and
// returns only the invoices still within their window (so an expired invoice is
// never polled or credited late).
func (p *Poller) sweepExpired(ctx context.Context, open []model.Invoice) []model.Invoice {
	now := p.now()
	active := make([]model.Invoice, 0, len(open))
	for _, inv := range open {
		if !inv.ExpiresAt.IsZero() && now.After(inv.ExpiresAt) {
			_ = p.store.SetInvoiceStatus(ctx, inv.ID, model.StatusExpired)
			continue
		}
		active = append(active, inv)
	}
	return active
}

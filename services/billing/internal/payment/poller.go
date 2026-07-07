package payment

import (
	"context"

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
}

// NewPoller wires the poller.
func NewPoller(repo store.Repository, registry *Registry, processor *Processor) *Poller {
	return &Poller{store: repo, registry: registry, processor: processor}
}

// RunOnce performs a single poll cycle. Errors on individual events are swallowed
// (logged by the caller) so one bad invoice cannot stall the rest.
func (p *Poller) RunOnce(ctx context.Context) error {
	open, err := p.store.OpenInvoices(ctx)
	if err != nil {
		return err
	}
	for _, g := range p.registry.Gateways() {
		events, err := g.Poll(ctx, open)
		if err != nil {
			continue
		}
		for _, ev := range events {
			_ = p.processor.Process(ctx, ev)
		}
	}
	return nil
}

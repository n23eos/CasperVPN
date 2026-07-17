package payment

import (
	"context"
	"time"

	"github.com/caspervpn/billing/internal/store"
)

// Recovery tunes the stuck-settlement reconciliation the poller runs each cycle.
type Recovery struct {
	// StaleThreshold is how old a settlement claim must be before recovery touches it.
	// It MUST exceed the activation HTTP timeout so a live in-flight settle is never
	// pre-empted; it only affects recovery latency, never correctness.
	StaleThreshold time.Duration
	// LeaseFor is how long a reconciler owns a stuck invoice before the lease can be
	// retaken (a crashed reconciler's rows become recoverable again after this).
	LeaseFor time.Duration
	// Batch caps how many stuck settlements one cycle recovers.
	Batch int
}

func (r Recovery) withDefaults() Recovery {
	if r.StaleThreshold <= 0 {
		r.StaleThreshold = 2 * time.Minute
	}
	if r.LeaseFor <= 0 {
		r.LeaseFor = time.Minute
	}
	if r.Batch <= 0 {
		r.Batch = 100
	}
	return r
}

// Poller drives poll-based gateways (e.g. direct on-chain watching). It reads the
// open invoices, asks every gateway for settlement events, and funnels them
// through the same Processor as webhooks — so idempotency holds regardless of
// whether a payment arrives by webhook or by poll.
type Poller struct {
	store     store.Repository
	registry  *Registry
	processor *Processor
	recovery  Recovery
	now       func() time.Time
}

// NewPoller wires the poller. now is injectable for deterministic tests.
func NewPoller(repo store.Repository, registry *Registry, processor *Processor, recovery Recovery, now func() time.Time) *Poller {
	if now == nil {
		now = time.Now
	}
	return &Poller{store: repo, registry: registry, processor: processor, recovery: recovery.withDefaults(), now: now}
}

// RunOnce performs a single poll cycle. Order matters:
//  1. Recover stuck settlements (crash between claim and completion) FIRST, so a
//     paid-but-uncredited invoice is finished — never buried by step 2.
//  2. Expire overdue invoices atomically, excluding any that carry a settlement
//     claim (an invoice mid-recovery must not be marked expired).
//  3. Poll the still-open invoices for settlement events.
//
// Errors on individual events (and a recovery pass) are swallowed so one bad invoice
// cannot stall the rest.
func (p *Poller) RunOnce(ctx context.Context) error {
	_ = p.processor.Reconcile(ctx, p.now().Add(-p.recovery.StaleThreshold), p.recovery.LeaseFor, p.recovery.Batch)

	if err := p.store.ExpireOverdue(ctx, p.now()); err != nil {
		return err
	}

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

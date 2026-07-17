package payment

import (
	"context"
	"fmt"
	"time"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/store"
)

// OnChain tunes the on-chain poll/expire policy (variant A: confirmed-before-deadline
// +grace). Grace has NO default and MUST be set explicitly when an on-chain gateway
// is registered (see Validate) — it is a per-chain/min-confirmations policy, not a
// universal constant.
type OnChain struct {
	Grace        time.Duration // extra window past ExpiresAt an on-chain confirmation is still accepted
	PollLease    time.Duration // how long a poll lease is held; MUST exceed ChainTimeout with margin
	ChainTimeout time.Duration // hard bound on each chain check
	Owner        string        // instance id — observability only, never an ownership mechanism
}

func (o OnChain) withDefaults() OnChain {
	if o.PollLease <= 0 {
		o.PollLease = 30 * time.Second
	}
	if o.ChainTimeout <= 0 {
		o.ChainTimeout = 10 * time.Second
	}
	if o.Owner == "" {
		o.Owner = "poller"
	}
	return o
}

// Validate is called at startup when an on-chain gateway is registered. Grace must be
// set explicitly, and the lease must outlast a chain check with margin (else a lease
// could lapse mid-check and let a second poller in).
func (o OnChain) Validate() error {
	if o.Grace <= 0 {
		return fmt.Errorf("billing: SETTLEMENT_GRACE must be set (>0) when an on-chain gateway is enabled")
	}
	if o.PollLease <= o.ChainTimeout {
		return fmt.Errorf("billing: POLL_LEASE (%s) must exceed CHAIN_CHECK_TIMEOUT (%s) with margin", o.PollLease, o.ChainTimeout)
	}
	return nil
}

// Recovery tunes the stuck-settlement reconciliation the poller runs each cycle.
type Recovery struct {
	// StaleThreshold is how old a settlement claim must be before recovery touches it.
	// It MUST exceed the activation HTTP timeout so a live in-flight settle is never
	// pre-empted; it only affects recovery latency, never correctness.
	StaleThreshold time.Duration
	// LeaseFor is how long a reconciler owns a stuck invoice before the lease can be
	// retaken (a crashed reconciler's rows become recoverable again after this). It
	// MUST exceed the worst-case finishSettlement wall time — the three control-plane
	// calls (GetUser + CreateSubscription + SetSubscriptionPeriod, each up to the ~10s
	// control-plane HTTP timeout ≈ 30s) plus the local store writes — or a second
	// reconciler could re-lease and double-activate mid-finish. Default 1m clears that;
	// raise it in lockstep if the control-plane timeout is ever raised.
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
	onchain   OnChain
	now       func() time.Time
}

// NewPoller wires the poller. now is injectable for deterministic tests.
func NewPoller(repo store.Repository, registry *Registry, processor *Processor, recovery Recovery, onchain OnChain, now func() time.Time) *Poller {
	if now == nil {
		now = time.Now
	}
	return &Poller{store: repo, registry: registry, processor: processor, recovery: recovery.withDefaults(), onchain: onchain.withDefaults(), now: now}
}

// RunOnce performs a single poll cycle. Order matters:
//  1. Recover stuck settlements (crash between claim and completion).
//  2. POLL the open invoices BEFORE expiring — an on-chain invoice is checked under a
//     durable poll lease so a concurrent sweep on another instance can't bury it, and
//     a definitive post-deadline negative check is recorded.
//  3. Expire overdue invoices: provider-aware effective deadline, and on-chain only
//     once a post-deadline negative check exists and there is no live lease/claim.
//
// Per-invoice/per-gateway errors are swallowed so one bad invoice can't stall the rest.
func (p *Poller) RunOnce(ctx context.Context) error {
	_ = p.processor.Reconcile(ctx, p.now().Add(-p.recovery.StaleThreshold), p.recovery.LeaseFor, p.recovery.Batch)

	open, err := p.store.OpenInvoices(ctx)
	if err != nil {
		return err
	}

	var onchainNames []string
	for _, g := range p.registry.Gateways() {
		if oc, ok := g.(OnChainGateway); ok {
			onchainNames = append(onchainNames, g.Name())
			p.pollOnChain(ctx, oc, open)
			continue
		}
		// Non-on-chain (webhook) gateways: real ones return nil here (they settle via
		// the webhook handler). Never poll-credit an invoice past its deadline — such a
		// gateway has no grace window, so an overdue order must not be settled by a poll.
		events, err := g.Poll(ctx, withinDeadline(open, p.now()))
		if err != nil {
			continue
		}
		for _, ev := range events {
			_ = p.processor.Process(ctx, ev)
		}
	}

	return p.store.ExpireOverdue(ctx, p.now(), onchainNames, p.onchain.Grace)
}

// withinDeadline returns the invoices whose (non-graced) deadline has not strictly
// passed — used for the webhook batch poll path so an overdue order is never settled
// by a poll.
func withinDeadline(open []model.Invoice, now time.Time) []model.Invoice {
	out := make([]model.Invoice, 0, len(open))
	for _, inv := range open {
		if inv.ExpiresAt.IsZero() || !now.After(inv.ExpiresAt) {
			out = append(out, inv)
		}
	}
	return out
}

// pollOnChain checks each of this gateway's open invoices under a durable poll lease.
func (p *Poller) pollOnChain(ctx context.Context, oc OnChainGateway, open []model.Invoice) {
	name := oc.Name()
	for _, inv := range open {
		if inv.Provider != name {
			continue
		}
		token, acquired, err := p.store.AcquirePollLease(ctx, inv.ID, p.onchain.Owner, p.onchain.PollLease)
		if err != nil || !acquired {
			continue // another instance owns the live lease
		}
		p.checkLeased(ctx, oc, inv, token)
	}
}

// checkLeased runs one bounded chain check while holding the lease, releasing it only
// AFTER the durable handoff (a positive is Process'd — its ClaimSettlement runs before
// release; a post-deadline negative is recorded). A chain API error records nothing.
func (p *Poller) checkLeased(ctx context.Context, oc OnChainGateway, inv model.Invoice, token string) {
	defer func() { _ = p.store.ReleasePollLease(ctx, inv.ID, token) }()

	cctx, cancel := context.WithTimeout(ctx, p.onchain.ChainTimeout)
	defer cancel()

	ev, negative, cerr := oc.CheckInvoice(cctx, inv)
	switch {
	case cerr != nil:
		return // chain API error — not a definitive negative, do not enable expiry
	case ev != nil:
		_ = p.processor.Process(ctx, *ev) // durable handoff: ClaimSettlement runs inside
	case negative:
		if deadline := inv.ExpiresAt.Add(p.onchain.Grace); !p.now().Before(deadline) {
			_ = p.store.RecordNegativeCheck(ctx, inv.ID, p.now())
		}
	}
}

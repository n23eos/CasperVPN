package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/money"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/billing/internal/subscription"
	"github.com/caspervpn/contracts"
)

// ErrStaleEvent rejects events older than the replay window.
var ErrStaleEvent = errors.New("payment: event outside replay window")

// ErrUnderpaid rejects a settlement whose paid amount is below the invoice amount.
var ErrUnderpaid = errors.New("payment: underpaid invoice")

// ErrProviderMismatch rejects an event whose provider differs from the invoice's.
// Without this, an event from gateway A could settle an invoice owned by gateway B.
var ErrProviderMismatch = errors.New("payment: event provider does not match invoice")

// ErrCurrencyMismatch rejects an event whose currency differs from the invoice's,
// so e.g. an XMR event can never "cover" a BTC invoice.
var ErrCurrencyMismatch = errors.New("payment: event currency does not match invoice")

// Processor applies normalized payment events to invoices and drives activation.
// It is the single choke point where idempotency and anti-fraud are enforced, so
// every source (webhook or poll) funnels through Process.
type Processor struct {
	store        store.Repository
	activator    *subscription.Activator
	replayWindow time.Duration
	now          func() time.Time
}

// NewProcessor wires the processor. replayWindow of 0 disables the age check.
func NewProcessor(repo store.Repository, activator *subscription.Activator, replayWindow time.Duration, now func() time.Time) *Processor {
	if now == nil {
		now = time.Now
	}
	return &Processor{store: repo, activator: activator, replayWindow: replayWindow, now: now}
}

// Process handles one event idempotently.
//
// Two guards combine:
//   - Delivery replay: a (provider, externalID) already processed to completion is
//     a no-op — this kills exact webhook replays.
//   - Settlement claim: crediting an invoice is latched by ClaimSettlement, so two
//     DISTINCT deliveries (or a webhook plus a poll) that both settle the same
//     invoice still add exactly one period. That is the "double webhook → no double
//     term" guarantee.
func (p *Processor) Process(ctx context.Context, ev model.Event) error {
	if p.replayWindow > 0 && !ev.Timestamp.IsZero() && p.now().Sub(ev.Timestamp) > p.replayWindow {
		return ErrStaleEvent
	}
	seen, err := p.store.SeenEvent(ctx, ev.Provider, ev.ExternalID)
	if err != nil {
		return fmt.Errorf("payment: seen check: %w", err)
	}
	if seen {
		return nil // exact delivery replay
	}

	switch ev.Status {
	case model.StatusSettled:
		if err := p.settle(ctx, ev); err != nil {
			return err // not recorded as processed — a retry can still succeed
		}
	case model.StatusExpired:
		_ = p.store.SetInvoiceStatus(ctx, ev.InvoiceID, model.StatusExpired)
	case model.StatusInvalid:
		_ = p.store.SetInvoiceStatus(ctx, ev.InvoiceID, model.StatusInvalid)
	default:
		return nil // pending / unknown — more events expected, don't record yet
	}

	return p.store.RecordEvent(ctx, ev.Provider, ev.ExternalID)
}

// Reconcile finishes settlements that were claimed but never completed — the crash
// window between the ClaimSettlement commit and the remote activation / status flip.
// It leases a batch cross-process-safely (so two reconcilers never touch the same
// invoice), and for each: if activation was already applied, only flips the invoice
// to settled; otherwise activates once, marks it, then flips. A per-invoice error is
// swallowed so one stuck invoice can't stall the batch — its lease expires and a
// later cycle retries. olderThan gates how stale a claim must be before recovery
// kicks in; it must exceed the activation HTTP timeout so a live in-flight settle is
// never pre-empted.
func (p *Processor) Reconcile(ctx context.Context, olderThan time.Time, leaseFor time.Duration, limit int) error {
	stuck, err := p.store.LeaseStuckSettlements(ctx, olderThan, leaseFor, limit)
	if err != nil {
		return fmt.Errorf("payment: lease stuck settlements: %w", err)
	}
	for _, s := range stuck {
		// A per-invoice failure is left claimed+leased; the lease expires and a later
		// cycle retries. It must not stop the rest of the batch.
		_ = p.finishSettlement(ctx, s)
	}
	return nil
}

// finishSettlement completes one crashed settlement. If the activation was already
// applied (Activated), it only flips the invoice to settled — re-activating would
// add a second period. Otherwise it activates once, marks it, then flips.
func (p *Processor) finishSettlement(ctx context.Context, s store.StuckSettlement) error {
	inv, err := p.store.GetInvoice(ctx, s.InvoiceID)
	if err != nil {
		return err
	}
	if inv.Status == model.StatusSettled {
		return nil
	}
	if !s.Activated {
		if _, err := p.activator.Activate(ctx, inv.AnonUserID, contracts.SubscriptionPlan(inv.Plan)); err != nil {
			return fmt.Errorf("payment: recover activate %q: %w", inv.ID, err)
		}
		// Best-effort marker; status=settled below is the durable guard against a
		// second period (see settle). A marker-write error must not leave the invoice
		// pending+unactivated, which would re-activate on the next pass.
		_ = p.store.MarkSettlementActivated(ctx, inv.ID)
	}
	return p.store.SetInvoiceStatus(ctx, inv.ID, model.StatusSettled)
}

func (p *Processor) settle(ctx context.Context, ev model.Event) error {
	inv, err := p.store.GetInvoice(ctx, ev.InvoiceID)
	if err != nil {
		return fmt.Errorf("payment: get invoice %q: %w", ev.InvoiceID, err)
	}

	// Anti-fraud: the settling event must belong to the same gateway and currency
	// as the invoice. Otherwise a cheap payment on one rail could close an
	// unrelated, more expensive invoice on another. Currency is only checked when
	// the event carries one (some webhook gateways settle on their own accounting).
	if ev.Provider != inv.Provider {
		return ErrProviderMismatch
	}
	if ev.Currency != "" && ev.Currency != inv.Currency {
		return ErrCurrencyMismatch
	}

	// Anti-fraud: reject underpayment. Only enforced when the event reports an
	// amount (webhook gateways may omit it and settle on their own accounting).
	if ev.Amount != "" {
		enough, err := money.GTE(ev.Amount, inv.Amount)
		if err != nil {
			return fmt.Errorf("payment: amount check: %w", err)
		}
		if !enough {
			_ = p.store.SetInvoiceStatus(ctx, inv.ID, model.StatusInvalid)
			return ErrUnderpaid
		}
	}

	// Atomic claim: only the first caller credits the invoice.
	claimed, err := p.store.ClaimSettlement(ctx, inv.ID)
	if err != nil {
		return fmt.Errorf("payment: claim settlement: %w", err)
	}
	if !claimed {
		return nil // already credited by an earlier delivery/poll
	}

	if _, err := p.activator.Activate(ctx, inv.AnonUserID, contracts.SubscriptionPlan(inv.Plan)); err != nil {
		// Back out the claim so a subsequent retry can credit it.
		_ = p.store.ReleaseSettlement(ctx, inv.ID)
		return fmt.Errorf("payment: activate: %w", err)
	}
	// Activation succeeded. The marker is a best-effort optimization (it lets recovery
	// skip re-activation if the status flip below is what crashes); the DURABLE guard
	// against a second period is status=settled, which finishSettlement short-circuits
	// on. So never bail on a marker-write error — bailing would leave pending +
	// activated_at=NULL, and a single transient DB blip here (not just a crash) would
	// then double-credit on the next recovery pass.
	_ = p.store.MarkSettlementActivated(ctx, inv.ID)
	return p.store.SetInvoiceStatus(ctx, inv.ID, model.StatusSettled)
}

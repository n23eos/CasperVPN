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

func (p *Processor) settle(ctx context.Context, ev model.Event) error {
	inv, err := p.store.GetInvoice(ctx, ev.InvoiceID)
	if err != nil {
		return fmt.Errorf("payment: get invoice %q: %w", ev.InvoiceID, err)
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
	return p.store.SetInvoiceStatus(ctx, inv.ID, model.StatusSettled)
}

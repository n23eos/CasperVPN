package payment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/controlplane"
	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/plan"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/billing/internal/subscription"
	"github.com/caspervpn/contracts"
)

const day = 24 * time.Hour

func newHarness(t *testing.T) (*Processor, *store.Memory, *controlplane.Fake) {
	t.Helper()
	fake := controlplane.NewFake()
	fake.AddUser(contracts.User{
		ID:             "acct-1",
		Status:         contracts.UserStatusActive,
		RealityShortID: "ab12",
		UUID:           "uuid-1",
	})
	catalog := plan.NewCatalog(plan.Plan{
		ID:       contracts.SubscriptionPlanBasic,
		Duration: 30 * day,
		Grace:    3 * day,
		Prices:   map[string]string{"BTC": "0.0001"},
	})
	repo := store.NewMemory()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	act := subscription.NewActivator(fake, catalog, repo, func() time.Time { return now })
	proc := NewProcessor(repo, act, 0, func() time.Time { return now })
	return proc, repo, fake
}

func seedInvoice(t *testing.T, repo *store.Memory) model.Invoice {
	t.Helper()
	inv := model.Invoice{
		ID:         "inv-1",
		Provider:   "mock",
		AnonUserID: "acct-1",
		Plan:       string(contracts.SubscriptionPlanBasic),
		Currency:   "BTC",
		Amount:     "0.0001",
		Status:     model.StatusPending,
	}
	if err := repo.CreateInvoice(context.Background(), inv); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	return inv
}

func settledEvent(externalID string) model.Event {
	return model.Event{
		Provider:   "mock",
		ExternalID: externalID,
		InvoiceID:  "inv-1",
		Status:     model.StatusSettled,
		Currency:   "BTC",
		Amount:     "0.0001",
	}
}

func TestProcess_SettlesAndActivatesOnce(t *testing.T) {
	proc, repo, fake := newHarness(t)
	seedInvoice(t, repo)

	if err := proc.Process(context.Background(), settledEvent("d1")); err != nil {
		t.Fatalf("process: %v", err)
	}

	if fake.CreateCalls != 1 {
		t.Fatalf("expected 1 subscription created, got %d", fake.CreateCalls)
	}
	inv, _ := repo.GetInvoice(context.Background(), "inv-1")
	if inv.Status != model.StatusSettled {
		t.Fatalf("invoice status = %q, want settled", inv.Status)
	}
}

// The headline anti-fraud guarantee: a double webhook must NOT grant a double term.
func TestProcess_DoubleWebhookGivesSingleTerm(t *testing.T) {
	proc, repo, fake := newHarness(t)
	seedInvoice(t, repo)

	// Two DISTINCT deliveries (different externalID) settling the SAME invoice.
	if err := proc.Process(context.Background(), settledEvent("delivery-A")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := proc.Process(context.Background(), settledEvent("delivery-B")); err != nil {
		t.Fatalf("second: %v", err)
	}

	// Exactly one crediting: one create, one SetPeriod — a single 30-day term.
	if fake.CreateCalls != 1 {
		t.Fatalf("create calls = %d, want 1 (no double credit)", fake.CreateCalls)
	}
	if fake.SetCalls != 1 {
		t.Fatalf("set-period calls = %d, want 1 (no double term)", fake.SetCalls)
	}
}

// An exact webhook replay (same externalID) is a plain no-op.
func TestProcess_ExactReplayIsNoop(t *testing.T) {
	proc, repo, fake := newHarness(t)
	seedInvoice(t, repo)

	ev := settledEvent("same-delivery")
	if err := proc.Process(context.Background(), ev); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := proc.Process(context.Background(), ev); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if fake.SetCalls != 1 {
		t.Fatalf("set-period calls = %d, want 1", fake.SetCalls)
	}
}

func TestProcess_RejectsUnderpayment(t *testing.T) {
	proc, repo, fake := newHarness(t)
	seedInvoice(t, repo)

	ev := settledEvent("d1")
	ev.Amount = "0.00005" // half the price
	err := proc.Process(context.Background(), ev)
	if !errors.Is(err, ErrUnderpaid) {
		t.Fatalf("err = %v, want ErrUnderpaid", err)
	}
	if fake.CreateCalls != 0 {
		t.Fatalf("underpaid invoice must not activate; create calls = %d", fake.CreateCalls)
	}
	inv, _ := repo.GetInvoice(context.Background(), "inv-1")
	if inv.Status != model.StatusInvalid {
		t.Fatalf("invoice status = %q, want invalid", inv.Status)
	}
}

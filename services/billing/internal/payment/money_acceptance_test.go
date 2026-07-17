package payment

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/contracts"
)

// Money acceptance: two DISTINCT settled invoices for the same user credit ONE
// subscription with TWO periods (expiry = start + 2×duration, no payment lost); a
// replay of one of those invoices adds zero.
func TestProcess_TwoDistinctInvoices_TwoPeriods_ReplayZero(t *testing.T) {
	proc, repo, fake := newHarness(t) // now = 2026-01-01, acct-1, Basic = 30d
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()

	for _, id := range []string{"inv-1", "inv-2"} {
		if err := repo.CreateInvoice(ctx, model.Invoice{
			ID: id, Provider: "mock", AnonUserID: "acct-1",
			Plan: string(contracts.SubscriptionPlanBasic), Currency: "BTC", Amount: "0.0001",
			Status: model.StatusPending,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	settle := func(delivery, invoiceID string) {
		ev := model.Event{Provider: "mock", ExternalID: delivery, InvoiceID: invoiceID, Status: model.StatusSettled, Currency: "BTC", Amount: "0.0001"}
		if err := proc.Process(ctx, ev); err != nil {
			t.Fatalf("settle %s: %v", invoiceID, err)
		}
	}

	settle("d1", "inv-1")
	settle("d2", "inv-2")

	if fake.CreateCalls != 1 {
		t.Fatalf("create calls = %d, want 1 (one subscription for the user)", fake.CreateCalls)
	}
	if fake.SetCalls != 2 {
		t.Fatalf("set-period calls = %d, want 2 (both invoices apply a period)", fake.SetCalls)
	}
	u, _ := fake.GetUser(ctx, "acct-1")
	s, _ := fake.Subscription(*u.SubscriptionID)
	if s.ExpiresAt == nil || !s.ExpiresAt.Equal(now.Add(2*30*day)) {
		t.Fatalf("expiry = %v, want %v (2 × 30 days, neither payment lost)", s.ExpiresAt, now.Add(2*30*day))
	}

	// Replay of inv-1's delivery: exact webhook replay is a no-op → no extra period.
	settle("d1", "inv-1")
	if fake.SetCalls != 2 {
		t.Fatalf("after replay set-period calls = %d, want 2 (+0)", fake.SetCalls)
	}
}

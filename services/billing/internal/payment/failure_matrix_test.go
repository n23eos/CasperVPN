package payment

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/controlplane"
	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/plan"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/billing/internal/subscription"
	"github.com/caspervpn/contracts"
)

// failScheduleStore fails the first UpsertSchedule, then behaves normally.
type failScheduleStore struct {
	store.Repository
	failNext bool
}

func (s *failScheduleStore) UpsertSchedule(ctx context.Context, sched model.Schedule) error {
	if s.failNext {
		s.failNext = false
		return fmt.Errorf("injected schedule write failure")
	}
	return s.Repository.UpsertSchedule(ctx, sched)
}

// Failure matrix: a schedule write failure aborts the settlement but KEEPS the claim
// (never releases it), so a concurrent sweep can't bury the confirmed-but-unactivated
// invoice; the durable Reconcile pass then finishes it with exactly ONE period —
// SetSubscriptionPeriod sets an absolute expiry, so re-applying is idempotent and no
// second subscription is created.
func TestProcess_ScheduleFailure_ReconcileCompletesNoDoublePeriod(t *testing.T) {
	fake := controlplane.NewFake()
	fake.AddUser(contracts.User{ID: "acct-1", Status: contracts.UserStatusActive, RealityShortID: "ab12", UUID: "u1"})
	catalog := plan.NewCatalog(plan.Plan{ID: contracts.SubscriptionPlanBasic, Duration: 30 * day, Grace: 3 * day, Prices: map[string]string{"BTC": "0.0001"}})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := &failScheduleStore{Repository: store.NewMemoryWithClock(func() time.Time { return now }), failNext: true}
	act := subscription.NewActivator(fake, catalog, repo, func() time.Time { return now })
	proc := NewProcessor(repo, act, 0, func() time.Time { return now })
	ctx := context.Background()

	if err := repo.CreateInvoice(ctx, model.Invoice{
		ID: "inv-1", Provider: "mock", AnonUserID: "acct-1",
		Plan: string(contracts.SubscriptionPlanBasic), Currency: "BTC", Amount: "0.0001", Status: model.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	ev := model.Event{Provider: "mock", ExternalID: "d1", InvoiceID: "inv-1", Status: model.StatusSettled, Currency: "BTC", Amount: "0.0001"}

	// Delivery: activation reaches the schedule write, which fails → error. The claim
	// is KEPT (a re-delivery would no-op; recovery owns completion).
	if err := proc.Process(ctx, ev); err == nil {
		t.Fatal("expected the injected schedule failure to surface")
	}
	if claimed, _ := repo.ClaimSettlement(ctx, "inv-1"); claimed {
		t.Fatal("claim was released on activation failure — must be kept for Reconcile")
	}

	// Durable recovery finishes it with one period.
	if _, err := proc.Reconcile(ctx, now, time.Minute, 100); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if fake.CreateCalls != 1 {
		t.Fatalf("create calls = %d, want 1 (no second subscription)", fake.CreateCalls)
	}
	u, _ := fake.GetUser(ctx, "acct-1")
	s, _ := fake.Subscription(*u.SubscriptionID)
	if s.ExpiresAt == nil || !s.ExpiresAt.Equal(now.Add(30*day)) {
		t.Fatalf("expiry = %v, want %v (exactly one period after recovery)", s.ExpiresAt, now.Add(30*day))
	}
}

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

// Failure matrix #4: a schedule write failure aborts the settlement; the retry must
// complete it with exactly ONE period — SetSubscriptionPeriod sets an absolute
// expiry, so re-applying the same period is idempotent, and no second subscription
// is created.
func TestProcess_ScheduleFailure_RetryNoDoublePeriod(t *testing.T) {
	fake := controlplane.NewFake()
	fake.AddUser(contracts.User{ID: "acct-1", Status: contracts.UserStatusActive, RealityShortID: "ab12", UUID: "u1"})
	catalog := plan.NewCatalog(plan.Plan{ID: contracts.SubscriptionPlanBasic, Duration: 30 * day, Grace: 3 * day, Prices: map[string]string{"BTC": "0.0001"}})
	repo := &failScheduleStore{Repository: store.NewMemory(), failNext: true}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
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

	// First delivery: activation reaches the schedule write, which fails → error, and
	// the settlement claim is released so a retry can re-credit.
	if err := proc.Process(ctx, ev); err == nil {
		t.Fatal("expected the injected schedule failure to surface")
	}
	// Retry (new delivery of the same invoice): completes with one period.
	ev.ExternalID = "d2"
	if err := proc.Process(ctx, ev); err != nil {
		t.Fatalf("retry: %v", err)
	}

	if fake.CreateCalls != 1 {
		t.Fatalf("create calls = %d, want 1 (no second subscription across retry)", fake.CreateCalls)
	}
	u, _ := fake.GetUser(ctx, "acct-1")
	s, _ := fake.Subscription(*u.SubscriptionID)
	if s.ExpiresAt == nil || !s.ExpiresAt.Equal(now.Add(30*day)) {
		t.Fatalf("expiry = %v, want %v (exactly one period after retry)", s.ExpiresAt, now.Add(30*day))
	}
}

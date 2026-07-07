package subscription

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/controlplane"
	"github.com/caspervpn/billing/internal/plan"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/contracts"
)

const day = 24 * time.Hour

func testCatalog() *plan.Catalog {
	return plan.NewCatalog(
		plan.Plan{
			ID:       contracts.SubscriptionPlanBasic,
			Duration: 30 * day,
			Grace:    3 * day,
			Prices:   map[string]string{"BTC": "0.0001"},
		},
	)
}

func seededFake(t *testing.T) (*controlplane.Fake, string) {
	t.Helper()
	fake := controlplane.NewFake()
	fake.AddUser(contracts.User{
		ID:             "acct-1",
		Status:         contracts.UserStatusActive,
		RealityShortID: "ab12",
		UUID:           "uuid-1",
	})
	return fake, "acct-1"
}

func TestActivate_FirstPurchaseCreatesSubscription(t *testing.T) {
	// Arrange
	fake, acct := seededFake(t)
	repo := store.NewMemory()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	act := NewActivator(fake, testCatalog(), repo, func() time.Time { return now })

	// Act
	sub, err := act.Activate(context.Background(), acct, contracts.SubscriptionPlanBasic)

	// Assert
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if fake.CreateCalls != 1 {
		t.Fatalf("expected 1 create call, got %d", fake.CreateCalls)
	}
	if sub.ExpiresAt == nil {
		t.Fatal("expected expiry set")
	}
	want := now.Add(30 * day)
	if !sub.ExpiresAt.Equal(want) {
		t.Fatalf("expiry = %v, want %v", sub.ExpiresAt, want)
	}
	if sub.Status != contracts.SubscriptionStatusActive {
		t.Fatalf("status = %q, want active", sub.Status)
	}
}

func TestActivate_RenewExtendsFromCurrentExpiry(t *testing.T) {
	// Arrange
	fake, acct := seededFake(t)
	repo := store.NewMemory()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	act := NewActivator(fake, testCatalog(), repo, func() time.Time { return start })

	first, err := act.Activate(context.Background(), acct, contracts.SubscriptionPlanBasic)
	if err != nil {
		t.Fatalf("first activate: %v", err)
	}

	// Act: renew 10 days later — should extend from the existing expiry, not now.
	renewNow := start.Add(10 * day)
	act.now = func() time.Time { return renewNow }
	second, err := act.Activate(context.Background(), acct, contracts.SubscriptionPlanBasic)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}

	// Assert: one subscription total, expiry = first expiry + 30d.
	if fake.CreateCalls != 1 {
		t.Fatalf("renew must not create a new subscription; create calls = %d", fake.CreateCalls)
	}
	want := first.ExpiresAt.Add(30 * day)
	if !second.ExpiresAt.Equal(want) {
		t.Fatalf("renew expiry = %v, want %v (extend from current expiry)", second.ExpiresAt, want)
	}
}

func TestActivate_RenewAfterLapseExtendsFromNow(t *testing.T) {
	// Arrange
	fake, acct := seededFake(t)
	repo := store.NewMemory()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	act := NewActivator(fake, testCatalog(), repo, func() time.Time { return start })
	if _, err := act.Activate(context.Background(), acct, contracts.SubscriptionPlanBasic); err != nil {
		t.Fatalf("first activate: %v", err)
	}

	// Act: renew long after expiry — base is now, not the stale past expiry.
	renewNow := start.Add(60 * day)
	act.now = func() time.Time { return renewNow }
	second, err := act.Activate(context.Background(), acct, contracts.SubscriptionPlanBasic)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}

	// Assert
	want := renewNow.Add(30 * day)
	if !second.ExpiresAt.Equal(want) {
		t.Fatalf("lapsed renew expiry = %v, want %v (extend from now)", second.ExpiresAt, want)
	}
}

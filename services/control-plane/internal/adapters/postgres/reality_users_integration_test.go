//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/postgres"
	"github.com/caspervpn/control-plane/internal/usecase"
)

// TestIntegration_EligibleRealityUsers pins the allow-list eligibility to exactly
// the subscription service's rule (resolve.go): user active AND subscription in
// {active,trialing,past_due} AND not past expires_at.
func TestIntegration_EligibleRealityUsers(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	users := postgres.NewUserStore(pool)
	subs := postgres.NewSubscriptionStore(pool)
	usvc := usecase.NewUserService(users, postgres.NewRotationStore(pool), noQueue{})
	ssvc := usecase.NewSubscriptionService(subs, users)

	future := time.Now().Add(24 * time.Hour).UTC()
	past := time.Now().Add(-time.Hour).UTC()
	active := contracts.SubscriptionStatusActive
	pastDue := contracts.SubscriptionStatusPastDue
	canceled := contracts.SubscriptionStatusCanceled

	// mkUser creates an active user with a subscription, then optionally patches
	// the subscription status/expiry. Returns the user's vless uuid.
	mkUser := func(t *testing.T, status *contracts.SubscriptionStatus, exp *time.Time) contracts.User {
		t.Helper()
		u, err := usvc.Create(ctx, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := ssvc.Create(ctx, u.ID, contracts.SubscriptionPlanBasic)
		if err != nil {
			t.Fatal(err)
		}
		if status != nil || exp != nil {
			if _, err := ssvc.Patch(ctx, res.Subscription.ID, contracts.SubscriptionPatch{Status: status, ExpiresAt: exp}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := usvc.Get(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	eligible1 := mkUser(t, nil, nil)          // active, open-ended -> eligible
	eligible2 := mkUser(t, &active, &future)  // active, future expiry -> eligible
	eligible3 := mkUser(t, &pastDue, &future) // past_due (grace) -> eligible
	_ = mkUser(t, &active, &past)             // expired -> NOT eligible
	_ = mkUser(t, &canceled, nil)             // canceled -> NOT eligible

	// suspended user with an otherwise-servable subscription -> NOT eligible
	susp := mkUser(t, nil, nil)
	susp.Status = contracts.UserStatusSuspended
	if _, err := usvc.Update(ctx, susp.ID, susp, "test"); err != nil {
		t.Fatal(err)
	}

	// Act
	got, err := users.EligibleRealityUsers(ctx)
	if err != nil {
		t.Fatalf("EligibleRealityUsers: %v", err)
	}

	// Assert: exactly the three eligible users, by uuid.
	want := map[string]bool{eligible1.UUID: true, eligible2.UUID: true, eligible3.UUID: true}
	if len(got) != len(want) {
		t.Fatalf("got %d eligible users, want %d: %+v", len(got), len(want), got)
	}
	for _, ru := range got {
		if !want[ru.UUID] {
			t.Errorf("unexpected user in allow-list: uuid=%s", ru.UUID)
		}
		if ru.ShortID == "" {
			t.Errorf("user %s has empty short_id", ru.UUID)
		}
	}
}

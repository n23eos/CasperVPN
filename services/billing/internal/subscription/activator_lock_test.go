package subscription

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/controlplane"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/contracts"
)

// Two DISTINCT settlements for the same user, run concurrently, must apply BOTH
// periods (expiry advances by 2×duration) against a SINGLE subscription — the
// per-user lock serializes the read-modify-write so neither period is lost.
func TestActivate_ConcurrentDistinctSettlements_TwoPeriods(t *testing.T) {
	fake := controlplane.NewFake()
	fake.AddUser(contracts.User{ID: "acct-1", Status: contracts.UserStatusActive, RealityShortID: "ab12", UUID: "u1"})
	repo := store.NewMemory()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	act := NewActivator(fake, testCatalog(), repo, func() time.Time { return now })

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := act.Activate(context.Background(), "acct-1", contracts.SubscriptionPlanBasic); err != nil {
				t.Errorf("activate: %v", err)
			}
		}()
	}
	wg.Wait()

	if fake.CreateCalls != 1 {
		t.Fatalf("create calls = %d, want 1 (one subscription)", fake.CreateCalls)
	}
	if fake.SetCalls != 2 {
		t.Fatalf("set-period calls = %d, want 2 (both periods applied)", fake.SetCalls)
	}
	u, _ := fake.GetUser(context.Background(), "acct-1")
	if u.SubscriptionID == nil {
		t.Fatal("user not linked")
	}
	s, _ := fake.Subscription(*u.SubscriptionID)
	want := now.Add(2 * 30 * day)
	if s.ExpiresAt == nil || !s.ExpiresAt.Equal(want) {
		t.Fatalf("expiry = %v, want %v (start + 2×duration, no lost period)", s.ExpiresAt, want)
	}
}

// conflictCP simulates a concurrent create winning between our GetUser (unlinked)
// and our CreateSubscription (409): the first GetUser reports no subscription,
// CreateSubscription returns ErrSubscriptionExists, and the second GetUser reports
// the subscription the winner linked.
type conflictCP struct {
	subID    string
	getCalls int
	SetCalls int
}

func (c *conflictCP) GetUser(_ context.Context, id string) (contracts.User, error) {
	c.getCalls++
	u := contracts.User{ID: id, Status: contracts.UserStatusActive}
	if c.getCalls >= 2 {
		sid := c.subID
		u.SubscriptionID = &sid
	}
	return u, nil
}
func (c *conflictCP) CreateSubscription(_ context.Context, _ string, _ contracts.SubscriptionPlan) (contracts.Subscription, error) {
	return contracts.Subscription{}, controlplane.ErrSubscriptionExists
}
func (c *conflictCP) SetSubscriptionPeriod(_ context.Context, subID string, status contracts.SubscriptionStatus, expiresAt time.Time) (contracts.Subscription, error) {
	c.SetCalls++
	exp := expiresAt
	return contracts.Subscription{ID: subID, Status: status, ExpiresAt: &exp}, nil
}

// On a create conflict the activator must re-read the user and reuse the existing
// subscription (never create a second), then apply the period once.
func TestActivate_CreateConflict_ReusesExisting(t *testing.T) {
	cp := &conflictCP{subID: "existing-sub"}
	repo := store.NewMemory()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	act := NewActivator(cp, testCatalog(), repo, func() time.Time { return now })

	sub, err := act.Activate(context.Background(), "acct-1", contracts.SubscriptionPlanBasic)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if sub.ID != "existing-sub" {
		t.Fatalf("sub id = %q, want the existing subscription", sub.ID)
	}
	if cp.SetCalls != 1 {
		t.Fatalf("set-period calls = %d, want 1 (period applied once)", cp.SetCalls)
	}
	if sub.ExpiresAt == nil || !sub.ExpiresAt.Equal(now.Add(30*day)) {
		t.Fatalf("expiry = %v, want %v (one period on the reused sub)", sub.ExpiresAt, now.Add(30*day))
	}
}

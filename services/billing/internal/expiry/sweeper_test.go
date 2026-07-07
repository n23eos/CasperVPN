package expiry

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/controlplane"
	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/contracts"
)

const day = 24 * time.Hour

func setup(t *testing.T, expiresAt, graceUntil time.Time) (*store.Memory, *controlplane.Fake, string) {
	t.Helper()
	fake := controlplane.NewFake()
	fake.AddUser(contracts.User{ID: "acct-1", Status: contracts.UserStatusActive, RealityShortID: "ab", UUID: "u"})
	sub, err := fake.CreateSubscription(context.Background(), "acct-1", contracts.SubscriptionPlanBasic)
	if err != nil {
		t.Fatalf("seed sub: %v", err)
	}
	repo := store.NewMemory()
	_ = repo.UpsertSchedule(context.Background(), model.Schedule{
		SubID:      sub.ID,
		AnonUserID: "acct-1",
		Status:     string(contracts.SubscriptionStatusActive),
		ExpiresAt:  expiresAt,
		GraceUntil: graceUntil,
	})
	return repo, fake, sub.ID
}

func TestSweep_ActiveStaysActiveBeforeExpiry(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo, fake, subID := setup(t, start.Add(30*day), start.Add(33*day))
	sw := NewSweeper(repo, fake, func() time.Time { return start.Add(10 * day) })

	if err := sw.RunOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	sub, _ := fake.Subscription(subID)
	if sub.Status != contracts.SubscriptionStatusActive {
		t.Fatalf("status = %q, want active", sub.Status)
	}
}

func TestSweep_PastExpiryEntersGrace(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo, fake, subID := setup(t, start.Add(30*day), start.Add(33*day))
	// One day past expiry, still inside the 3-day grace window.
	sw := NewSweeper(repo, fake, func() time.Time { return start.Add(31 * day) })

	if err := sw.RunOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	sub, _ := fake.Subscription(subID)
	if sub.Status != contracts.SubscriptionStatusPastDue {
		t.Fatalf("status = %q, want past_due", sub.Status)
	}
}

func TestSweep_PastGraceExpires(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo, fake, subID := setup(t, start.Add(30*day), start.Add(33*day))
	// Past the grace window.
	sw := NewSweeper(repo, fake, func() time.Time { return start.Add(40 * day) })

	if err := sw.RunOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	sub, _ := fake.Subscription(subID)
	if sub.Status != contracts.SubscriptionStatusExpired {
		t.Fatalf("status = %q, want expired", sub.Status)
	}
}

func TestSweep_Idempotent(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo, fake, subID := setup(t, start.Add(30*day), start.Add(33*day))
	sw := NewSweeper(repo, fake, func() time.Time { return start.Add(40 * day) })

	_ = sw.RunOnce(context.Background())
	callsAfterFirst := fake.SetCalls
	_ = sw.RunOnce(context.Background())

	if fake.SetCalls != callsAfterFirst {
		t.Fatalf("second sweep changed state: set calls %d -> %d", callsAfterFirst, fake.SetCalls)
	}
	sub, _ := fake.Subscription(subID)
	if sub.Status != contracts.SubscriptionStatusExpired {
		t.Fatalf("status = %q, want expired", sub.Status)
	}
}

package payment_test

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/controlplane"
	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/payment"
	"github.com/caspervpn/billing/internal/plan"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/billing/internal/subscription"
	"github.com/caspervpn/contracts"
)

const day = 24 * time.Hour

// noopRecoveryLog is the required RecoveryLogger for pollers whose test doesn't assert
// on recovery output.
func noopRecoveryLog(payment.RecoveryReport) {}

// stubGateway is a poll-based gateway that reports one settled event per open
// invoice with a stable ExternalID (like the on-chain watcher).
type stubGateway struct{}

func (stubGateway) Name() string                 { return "stub" }
func (stubGateway) SupportsCurrency(string) bool { return true }
func (stubGateway) CreateInvoice(context.Context, model.CreateInvoiceRequest) (model.Invoice, error) {
	return model.Invoice{}, nil
}
func (stubGateway) ParseWebhook(string, []byte) (model.Event, error) {
	return model.Event{}, payment.ErrUnsupportedWebhook
}
func (stubGateway) Poll(_ context.Context, open []model.Invoice) ([]model.Event, error) {
	var evs []model.Event
	for _, inv := range open {
		evs = append(evs, model.Event{
			Provider: "stub", ExternalID: "stub:" + inv.ID, InvoiceID: inv.ID,
			Status: model.StatusSettled, Currency: inv.Currency, Amount: inv.Amount,
		})
	}
	return evs, nil
}

// C6: a pending invoice past its ExpiresAt is swept to expired on the next poll
// cycle and is not polled/credited afterwards.
func TestPoller_SweepsExpiredPending(t *testing.T) {
	fake := controlplane.NewFake()
	fake.AddUser(contracts.User{ID: "acct-1", Status: contracts.UserStatusActive, RealityShortID: "ab", UUID: "u"})
	catalog := plan.NewCatalog(plan.Plan{
		ID: contracts.SubscriptionPlanBasic, Duration: 30 * day, Grace: 3 * day,
		Prices: map[string]string{"BTC": "0.0001"},
	})
	repo := store.NewMemory()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	nowAfterExpiry := created.Add(2 * time.Hour)

	act := subscription.NewActivator(fake, catalog, repo, func() time.Time { return created })
	proc := payment.NewProcessor(repo, act, 0, func() time.Time { return created })
	reg := payment.NewRegistry()
	reg.Register(stubGateway{}) // would settle any open invoice if not swept first
	poller := payment.NewPoller(repo, reg, proc, payment.Recovery{}, payment.OnChain{}, noopRecoveryLog, func() time.Time { return nowAfterExpiry })

	_ = repo.CreateInvoice(context.Background(), model.Invoice{
		ID: "inv-1", Provider: "stub", AnonUserID: "acct-1",
		Plan: string(contracts.SubscriptionPlanBasic), Currency: "BTC", Amount: "0.0001",
		Status: model.StatusPending, CreatedAt: created, ExpiresAt: created.Add(time.Hour),
	})

	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}

	inv, _ := repo.GetInvoice(context.Background(), "inv-1")
	if inv.Status != model.StatusExpired {
		t.Fatalf("invoice status = %q, want expired", inv.Status)
	}
	// The expired invoice must NOT have been credited by the stub gateway.
	if fake.CreateCalls != 0 || fake.SetCalls != 0 {
		t.Fatalf("expired invoice credited: create=%d set=%d, want 0/0", fake.CreateCalls, fake.SetCalls)
	}
}

// Polling the same paid invoice twice must credit it exactly once.
func TestPoller_RepeatedPollCreditsOnce(t *testing.T) {
	fake := controlplane.NewFake()
	fake.AddUser(contracts.User{ID: "acct-1", Status: contracts.UserStatusActive, RealityShortID: "ab", UUID: "u"})
	catalog := plan.NewCatalog(plan.Plan{
		ID: contracts.SubscriptionPlanBasic, Duration: 30 * day, Grace: 3 * day,
		Prices: map[string]string{"BTC": "0.0001"},
	})
	repo := store.NewMemory()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	act := subscription.NewActivator(fake, catalog, repo, func() time.Time { return now })
	proc := payment.NewProcessor(repo, act, 0, func() time.Time { return now })

	reg := payment.NewRegistry()
	reg.Register(stubGateway{})
	poller := payment.NewPoller(repo, reg, proc, payment.Recovery{}, payment.OnChain{}, noopRecoveryLog, func() time.Time { return now })

	_ = repo.CreateInvoice(context.Background(), model.Invoice{
		ID: "inv-1", Provider: "stub", AnonUserID: "acct-1",
		Plan: string(contracts.SubscriptionPlanBasic), Currency: "BTC", Amount: "0.0001",
		Status: model.StatusPending,
	})

	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	// Second cycle: the invoice is now settled, so it's no longer "open" and the
	// stable ExternalID would dedup regardless.
	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("poll 2: %v", err)
	}

	if fake.CreateCalls != 1 || fake.SetCalls != 1 {
		t.Fatalf("create=%d set=%d, want 1/1 (single credit)", fake.CreateCalls, fake.SetCalls)
	}
}

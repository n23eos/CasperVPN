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

func TestOnChain_Validate(t *testing.T) {
	if err := (payment.OnChain{PollLease: 30 * time.Second, ChainTimeout: 10 * time.Second}).Validate(); err == nil {
		t.Error("expected error when SETTLEMENT_GRACE is unset for on-chain")
	}
	if err := (payment.OnChain{Grace: time.Minute, PollLease: 5 * time.Second, ChainTimeout: 10 * time.Second}).Validate(); err == nil {
		t.Error("expected error when POLL_LEASE <= CHAIN_CHECK_TIMEOUT")
	}
	if err := (payment.OnChain{Grace: time.Minute, PollLease: 30 * time.Second, ChainTimeout: 10 * time.Second}).Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

type checkResult struct {
	ev       *model.Event
	negative bool
	err      error
}

type fakeOnChain struct{ results map[string]checkResult }

func (fakeOnChain) Name() string                 { return "onchain" }
func (fakeOnChain) SupportsCurrency(string) bool { return true }
func (fakeOnChain) CreateInvoice(context.Context, model.CreateInvoiceRequest) (model.Invoice, error) {
	return model.Invoice{}, nil
}
func (fakeOnChain) ParseWebhook(string, []byte) (model.Event, error) {
	return model.Event{}, payment.ErrUnsupportedWebhook
}
func (f fakeOnChain) Poll(ctx context.Context, open []model.Invoice) ([]model.Event, error) {
	return nil, nil
}
func (f fakeOnChain) CheckInvoice(_ context.Context, inv model.Invoice) (*model.Event, bool, error) {
	r := f.results[inv.ID]
	return r.ev, r.negative, r.err
}

func newOnChainPoller(t *testing.T, gw fakeOnChain, now time.Time) (*payment.Poller, *store.Memory, *controlplane.Fake) {
	t.Helper()
	fake := controlplane.NewFake()
	fake.AddUser(contracts.User{ID: "acct-1", Status: contracts.UserStatusActive, RealityShortID: "ab", UUID: "u"})
	catalog := plan.NewCatalog(plan.Plan{ID: contracts.SubscriptionPlanBasic, Duration: 30 * 24 * time.Hour, Grace: 3 * 24 * time.Hour, Prices: map[string]string{"BTC": "0.0001"}})
	repo := store.NewMemoryWithClock(func() time.Time { return now })
	act := subscription.NewActivator(fake, catalog, repo, func() time.Time { return now })
	proc := payment.NewProcessor(repo, act, 0, func() time.Time { return now })
	reg := payment.NewRegistry()
	reg.Register(gw)
	oc := payment.OnChain{Grace: 15 * time.Minute, PollLease: 30 * time.Second, ChainTimeout: 10 * time.Second, Owner: "test"}
	return payment.NewPoller(repo, reg, proc, payment.Recovery{}, oc, func() time.Time { return now }), repo, fake
}

func seedOnChain(t *testing.T, repo *store.Memory, id string, expiresAt time.Time) {
	t.Helper()
	if err := repo.CreateInvoice(context.Background(), model.Invoice{
		ID: id, Provider: "onchain", AnonUserID: "acct-1", Plan: string(contracts.SubscriptionPlanBasic),
		Currency: "BTC", Amount: "0.0001", Status: model.StatusPending, ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatal(err)
	}
}

// A definitive negative past the deadline is recorded and the invoice is expired in
// the same cycle (poll records the negative, then the sweep runs).
func TestPoller_OnChain_NegativePastDeadlineExpires(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	gw := fakeOnChain{results: map[string]checkResult{"inv-1": {negative: true}}}
	poller, repo, _ := newOnChainPoller(t, gw, now)
	seedOnChain(t, repo, "inv-1", now.Add(-20*time.Minute)) // deadline now-5m

	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	inv, _ := repo.GetInvoice(context.Background(), "inv-1")
	if inv.Status != model.StatusExpired {
		t.Fatalf("status = %q, want expired (post-deadline negative)", inv.Status)
	}
}

// A negative WITHIN the grace window records nothing and never expires.
func TestPoller_OnChain_NegativeWithinGraceStaysPending(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	gw := fakeOnChain{results: map[string]checkResult{"inv-1": {negative: true}}}
	poller, repo, _ := newOnChainPoller(t, gw, now)
	seedOnChain(t, repo, "inv-1", now.Add(-5*time.Minute)) // deadline now+10m → within grace

	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	inv, _ := repo.GetInvoice(context.Background(), "inv-1")
	if inv.Status != model.StatusPending {
		t.Fatalf("status = %q, want pending (still within grace)", inv.Status)
	}
}

// A positive check settles the invoice (never expired), even past the deadline.
func TestPoller_OnChain_PositiveSettles(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	gw := fakeOnChain{results: map[string]checkResult{"inv-1": {ev: &model.Event{
		Provider: "onchain", ExternalID: "onchain:inv-1", InvoiceID: "inv-1",
		Status: model.StatusSettled, Currency: "BTC", Amount: "0.0001",
	}}}}
	poller, repo, fake := newOnChainPoller(t, gw, now)
	seedOnChain(t, repo, "inv-1", now.Add(-20*time.Minute)) // past deadline, but positive settles

	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	inv, _ := repo.GetInvoice(context.Background(), "inv-1")
	if inv.Status != model.StatusSettled {
		t.Fatalf("status = %q, want settled", inv.Status)
	}
	if fake.CreateCalls != 1 || fake.SetCalls != 1 {
		t.Fatalf("credit: create=%d set=%d, want 1/1", fake.CreateCalls, fake.SetCalls)
	}
}

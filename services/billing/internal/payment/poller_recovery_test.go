package payment_test

import (
	"context"
	"errors"
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

func basicCatalog() *plan.Catalog {
	return plan.NewCatalog(plan.Plan{
		ID: contracts.SubscriptionPlanBasic, Duration: 30 * day, Grace: 3 * day,
		Prices: map[string]string{"BTC": "0.0001"},
	})
}

// Full path Reconcile → Poller → logger callback: a stuck settlement is recovered and
// the cycle is logged exactly once with a structured report.
func TestPoller_RecoveryLoggedOncePerCycle(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	storeClock := func() time.Time { return base }
	fake := controlplane.NewFake()
	fake.AddUser(contracts.User{ID: "acct-1", Status: contracts.UserStatusActive, RealityShortID: "ab", UUID: "u"})
	repo := store.NewMemoryWithClock(storeClock)
	act := subscription.NewActivator(fake, basicCatalog(), repo, storeClock)
	proc := payment.NewProcessor(repo, act, 0, storeClock)
	ctx := context.Background()

	// A claim committed but never activated = a stuck settlement.
	_ = repo.CreateInvoice(ctx, model.Invoice{
		ID: "inv-1", Provider: "mock", AnonUserID: "acct-1",
		Plan: string(contracts.SubscriptionPlanBasic), Currency: "BTC", Amount: "0.0001",
		Status: model.StatusPending, ExpiresAt: base.Add(time.Hour),
	})
	if _, err := repo.ClaimSettlement(ctx, "inv-1"); err != nil {
		t.Fatal(err)
	}

	var reports []payment.RecoveryReport
	logger := func(r payment.RecoveryReport) { reports = append(reports, r) }
	// now past the 2m default stale threshold so the claim is recoverable.
	poller := payment.NewPoller(repo, payment.NewRegistry(), proc, payment.Recovery{}, payment.OnChain{},
		logger, func() time.Time { return base.Add(3 * time.Minute) })

	if err := poller.RunOnce(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("logger called %d times, want exactly 1", len(reports))
	}
	if reports[0].Recovered != 1 {
		t.Fatalf("report = %+v, want Recovered=1", reports[0])
	}
	if inv, _ := repo.GetInvoice(ctx, "inv-1"); inv.Status != model.StatusSettled {
		t.Fatalf("invoice = %q, want settled", inv.Status)
	}
}

// An idle cycle with no recovery work logs nothing, so the poll loop stays quiet.
func TestPoller_EmptyCycleNotLogged(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := store.NewMemoryWithClock(func() time.Time { return base })
	proc := payment.NewProcessor(repo, nil, 0, func() time.Time { return base })

	calls := 0
	logger := func(payment.RecoveryReport) { calls++ }
	poller := payment.NewPoller(repo, payment.NewRegistry(), proc, payment.Recovery{}, payment.OnChain{},
		logger, func() time.Time { return base })

	if err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if calls != 0 {
		t.Fatalf("logger called %d times on an empty cycle, want 0", calls)
	}
}

// pollLeaseErrRepo fails the recovery lease query but serves everything else, so the
// cycle's poll/expire work must still run.
type pollLeaseErrRepo struct {
	store.Repository
}

func (pollLeaseErrRepo) LeaseStuckSettlements(context.Context, time.Time, time.Duration, int) ([]store.StuckSettlement, error) {
	return nil, errors.New("lease query down")
}

// A recovery failure is logged once but must not stop the sweep: an overdue invoice is
// still expired in the same cycle.
func TestPoller_RecoveryFailureDoesNotStopPollExpire(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := &pollLeaseErrRepo{Repository: store.NewMemoryWithClock(func() time.Time { return base })}
	proc := payment.NewProcessor(repo, nil, 0, func() time.Time { return base })
	ctx := context.Background()

	_ = repo.CreateInvoice(ctx, model.Invoice{
		ID: "inv-1", Provider: "mock", AnonUserID: "acct-1",
		Plan: string(contracts.SubscriptionPlanBasic), Currency: "BTC", Amount: "0.0001",
		Status: model.StatusPending, ExpiresAt: base.Add(-time.Hour), // already overdue
	})

	var reports []payment.RecoveryReport
	logger := func(r payment.RecoveryReport) { reports = append(reports, r) }
	poller := payment.NewPoller(repo, payment.NewRegistry(), proc, payment.Recovery{}, payment.OnChain{},
		logger, func() time.Time { return base })

	if err := poller.RunOnce(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(reports) != 1 || reports[0].Stages["lease_query"] != 1 {
		t.Fatalf("reports = %+v, want one lease_query cycle", reports)
	}
	if inv, _ := repo.GetInvoice(ctx, "inv-1"); inv.Status != model.StatusExpired {
		t.Fatalf("invoice = %q, want expired (sweep must still run after a recovery failure)", inv.Status)
	}
}

// A nil RecoveryLogger is a programmer error, not a silent no-op.
func TestNewPoller_NilLoggerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected NewPoller to panic on a nil RecoveryLogger")
		}
	}()
	payment.NewPoller(store.NewMemory(), payment.NewRegistry(), nil, payment.Recovery{}, payment.OnChain{}, nil, time.Now)
}

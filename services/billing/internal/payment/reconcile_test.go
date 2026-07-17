package payment

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/controlplane"
	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/plan"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/billing/internal/subscription"
	"github.com/caspervpn/contracts"
)

// The recovery clock is shared by the store (stamps claimed_at / lease expiry) and
// the processor/activator, so a stuck-settlement's age is fully deterministic.
type recoverHarness struct {
	proc *Processor
	repo *store.Memory
	fake *controlplane.Fake
	clk  *time.Time
}

func newRecoverHarness(t *testing.T, cp controlplane.Client) *recoverHarness {
	t.Helper()
	clk := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return clk }

	fake, _ := cp.(*controlplane.Fake)
	catalog := plan.NewCatalog(plan.Plan{
		ID:       contracts.SubscriptionPlanBasic,
		Duration: 30 * day,
		Grace:    3 * day,
		Prices:   map[string]string{"BTC": "0.0001"},
	})
	repo := store.NewMemoryWithClock(func() time.Time { return clk })
	act := subscription.NewActivator(cp, catalog, repo, now)
	proc := NewProcessor(repo, act, 0, now)
	return &recoverHarness{proc: proc, repo: repo, fake: fake, clk: &clk}
}

func seededFakeCP(t *testing.T, accts ...string) *controlplane.Fake {
	t.Helper()
	fake := controlplane.NewFake()
	for _, a := range accts {
		fake.AddUser(contracts.User{ID: a, Status: contracts.UserStatusActive, RealityShortID: "ab12", UUID: "uuid-" + a})
	}
	return fake
}

func seedPending(t *testing.T, repo *store.Memory, id, acct string, expiresAt time.Time) {
	t.Helper()
	inv := model.Invoice{
		ID:         id,
		Provider:   "mock",
		AnonUserID: acct,
		Plan:       string(contracts.SubscriptionPlanBasic),
		Currency:   "BTC",
		Amount:     "0.0001",
		Status:     model.StatusPending,
		ExpiresAt:  expiresAt,
	}
	if err := repo.CreateInvoice(context.Background(), inv); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func statusOf(t *testing.T, repo *store.Memory, id string) model.Status {
	t.Helper()
	inv, err := repo.GetInvoice(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return inv.Status
}

func subExpiry(t *testing.T, fake *controlplane.Fake, acct string) time.Time {
	t.Helper()
	u, err := fake.GetUser(context.Background(), acct)
	if err != nil {
		t.Fatalf("get user %s: %v", acct, err)
	}
	if u.SubscriptionID == nil || *u.SubscriptionID == "" {
		t.Fatalf("user %s has no subscription", acct)
	}
	s, ok := fake.Subscription(*u.SubscriptionID)
	if !ok || s.ExpiresAt == nil {
		t.Fatalf("subscription for %s missing/no expiry", acct)
	}
	return *s.ExpiresAt
}

const (
	leaseFor = 30 * time.Second
	batch    = 50
)

// (a) A claim committed but never activated (crash before Activate) is healed:
// activated once, invoice settled, exactly one period.
func TestReconcile_HealsCrashedBeforeActivate(t *testing.T) {
	h := newRecoverHarness(t, seededFakeCP(t, "acct-1"))
	seedPending(t, h.repo, "inv-1", "acct-1", h.clk.Add(time.Hour))

	claimed, err := h.repo.ClaimSettlement(context.Background(), "inv-1")
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}

	if err := h.proc.Reconcile(context.Background(), *h.clk, leaseFor, batch); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if h.fake.CreateCalls != 1 || h.fake.SetCalls != 1 {
		t.Fatalf("want create=1 set=1, got create=%d set=%d", h.fake.CreateCalls, h.fake.SetCalls)
	}
	if got := statusOf(t, h.repo, "inv-1"); got != model.StatusSettled {
		t.Fatalf("status = %q, want settled", got)
	}
	if got, want := subExpiry(t, h.fake, "acct-1"), h.clk.Add(30*day); !got.Equal(want) {
		t.Fatalf("expiry = %v, want %v (one period)", got, want)
	}
}

// (b) A fully settled invoice is not re-processed (nothing to lease).
func TestReconcile_SettledIsNoop(t *testing.T) {
	h := newRecoverHarness(t, seededFakeCP(t, "acct-1"))
	seedPending(t, h.repo, "inv-1", "acct-1", h.clk.Add(time.Hour))
	if err := h.proc.Process(context.Background(), settledEvent("d1")); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if statusOf(t, h.repo, "inv-1") != model.StatusSettled {
		t.Fatal("precondition: invoice should be settled")
	}
	before := h.fake.SetCalls

	if err := h.proc.Reconcile(context.Background(), *h.clk, leaseFor, batch); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if h.fake.SetCalls != before {
		t.Fatalf("set-period calls = %d, want unchanged %d", h.fake.SetCalls, before)
	}
}

// (c) Age gate: a claim younger than the recovery cutoff is left alone; once old
// enough it is recovered.
func TestReconcile_AgeGate(t *testing.T) {
	h := newRecoverHarness(t, seededFakeCP(t, "acct-1"))
	seedPending(t, h.repo, "inv-1", "acct-1", h.clk.Add(time.Hour))
	if _, err := h.repo.ClaimSettlement(context.Background(), "inv-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Cutoff strictly before the claim time — must NOT touch it.
	if err := h.proc.Reconcile(context.Background(), h.clk.Add(-time.Second), leaseFor, batch); err != nil {
		t.Fatalf("reconcile early: %v", err)
	}
	if got := statusOf(t, h.repo, "inv-1"); got != model.StatusPending {
		t.Fatalf("status = %q, want still pending (below cutoff)", got)
	}
	if h.fake.CreateCalls != 0 {
		t.Fatalf("create calls = %d, want 0 below cutoff", h.fake.CreateCalls)
	}

	// Cutoff at/after the claim time — recover it.
	if err := h.proc.Reconcile(context.Background(), *h.clk, leaseFor, batch); err != nil {
		t.Fatalf("reconcile due: %v", err)
	}
	if got := statusOf(t, h.repo, "inv-1"); got != model.StatusSettled {
		t.Fatalf("status = %q, want settled (past cutoff)", got)
	}
}

// (d) A transient activation failure leaves the invoice pending; a later pass (after
// the lease expires) completes it — with a single term, no double-create.
func TestReconcile_TransientFailureRetries(t *testing.T) {
	fake := seededFakeCP(t, "acct-1")
	flaky := &flakyOnceCP{Fake: fake}
	h := newRecoverHarness(t, flaky)
	seedPending(t, h.repo, "inv-1", "acct-1", h.clk.Add(time.Hour))
	if _, err := h.repo.ClaimSettlement(context.Background(), "inv-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Pass 1: activation fails inside; invoice stays pending.
	if err := h.proc.Reconcile(context.Background(), *h.clk, leaseFor, batch); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if got := statusOf(t, h.repo, "inv-1"); got != model.StatusPending {
		t.Fatalf("after failed pass status = %q, want pending", got)
	}

	// Advance past the lease so the row is re-leasable, then retry — now succeeds.
	*h.clk = h.clk.Add(2 * leaseFor)
	if err := h.proc.Reconcile(context.Background(), *h.clk, leaseFor, batch); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if got := statusOf(t, h.repo, "inv-1"); got != model.StatusSettled {
		t.Fatalf("after retry status = %q, want settled", got)
	}
	if fake.CreateCalls != 1 {
		t.Fatalf("create calls = %d, want 1 (no double create across retry)", fake.CreateCalls)
	}
}

// (e) Two reconcilers racing the same stuck invoice credit it exactly once.
func TestReconcile_ConcurrentSingleTerm(t *testing.T) {
	fake := seededFakeCP(t, "acct-1")
	h := newRecoverHarness(t, fake)
	seedPending(t, h.repo, "inv-1", "acct-1", h.clk.Add(time.Hour))
	if _, err := h.repo.ClaimSettlement(context.Background(), "inv-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.proc.Reconcile(context.Background(), *h.clk, leaseFor, batch)
		}()
	}
	wg.Wait()

	if fake.CreateCalls != 1 || fake.SetCalls != 1 {
		t.Fatalf("want create=1 set=1 under concurrency, got create=%d set=%d", fake.CreateCalls, fake.SetCalls)
	}
	if got := statusOf(t, h.repo, "inv-1"); got != model.StatusSettled {
		t.Fatalf("status = %q, want settled", got)
	}
	if got, want := subExpiry(t, h.fake, "acct-1"), h.clk.Add(30*day); !got.Equal(want) {
		t.Fatalf("expiry = %v, want %v (single term)", got, want)
	}
}

// (f) A crash AFTER activation but before the status flip: recovery finishes the
// status WITHOUT applying the period a second time.
func TestReconcile_CrashAfterActivateNoDoublePeriod(t *testing.T) {
	fake := seededFakeCP(t, "acct-1")
	h := newRecoverHarness(t, fake)
	seedPending(t, h.repo, "inv-1", "acct-1", h.clk.Add(time.Hour))
	if _, err := h.repo.ClaimSettlement(context.Background(), "inv-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Simulate "activation applied, status not yet flipped": run the activation and
	// mark it, but leave the invoice pending.
	act := subscription.NewActivator(fake, plan.NewCatalog(plan.Plan{
		ID: contracts.SubscriptionPlanBasic, Duration: 30 * day, Grace: 3 * day,
		Prices: map[string]string{"BTC": "0.0001"},
	}), h.repo, func() time.Time { return *h.clk })
	if _, err := act.Activate(context.Background(), "acct-1", contracts.SubscriptionPlanBasic); err != nil {
		t.Fatalf("simulated activate: %v", err)
	}
	if err := h.repo.MarkSettlementActivated(context.Background(), "inv-1"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	wantExpiry := subExpiry(t, fake, "acct-1")
	setBefore := fake.SetCalls

	if err := h.proc.Reconcile(context.Background(), *h.clk, leaseFor, batch); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if got := statusOf(t, h.repo, "inv-1"); got != model.StatusSettled {
		t.Fatalf("status = %q, want settled", got)
	}
	if fake.SetCalls != setBefore {
		t.Fatalf("set-period calls = %d, want unchanged %d (no re-extend)", fake.SetCalls, setBefore)
	}
	if got := subExpiry(t, fake, "acct-1"); !got.Equal(wantExpiry) {
		t.Fatalf("expiry = %v, want unchanged %v (no double period)", got, wantExpiry)
	}
}

// (h) One invoice failing to recover must not block the others in the batch.
func TestReconcile_OneFailureDoesNotBlockRest(t *testing.T) {
	// acct-good exists; acct-missing is never added → its activation fails on GetUser.
	fake := seededFakeCP(t, "acct-good")
	h := newRecoverHarness(t, fake)
	seedPending(t, h.repo, "inv-bad", "acct-missing", h.clk.Add(time.Hour))
	seedPending(t, h.repo, "inv-good", "acct-good", h.clk.Add(time.Hour))
	for _, id := range []string{"inv-bad", "inv-good"} {
		if _, err := h.repo.ClaimSettlement(context.Background(), id); err != nil {
			t.Fatalf("claim %s: %v", id, err)
		}
	}

	if err := h.proc.Reconcile(context.Background(), *h.clk, leaseFor, batch); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if got := statusOf(t, h.repo, "inv-good"); got != model.StatusSettled {
		t.Fatalf("inv-good status = %q, want settled (not blocked by inv-bad)", got)
	}
	if got := statusOf(t, h.repo, "inv-bad"); got != model.StatusPending {
		t.Fatalf("inv-bad status = %q, want still pending", got)
	}
}

// (g) An overdue invoice that carries a settlement claim is never expired.
func TestExpireOverdue_SkipsClaimed(t *testing.T) {
	h := newRecoverHarness(t, seededFakeCP(t, "acct-1"))
	// Two overdue pending invoices; only inv-claimed carries a claim.
	seedPending(t, h.repo, "inv-claimed", "acct-1", *h.clk)
	seedPending(t, h.repo, "inv-open", "acct-1", *h.clk)
	if _, err := h.repo.ClaimSettlement(context.Background(), "inv-claimed"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	*h.clk = h.clk.Add(time.Hour)
	if err := h.repo.ExpireOverdue(context.Background(), *h.clk); err != nil {
		t.Fatalf("expire: %v", err)
	}

	if got := statusOf(t, h.repo, "inv-claimed"); got != model.StatusPending {
		t.Fatalf("claimed invoice status = %q, want still pending (never buried)", got)
	}
	if got := statusOf(t, h.repo, "inv-open"); got != model.StatusExpired {
		t.Fatalf("unclaimed overdue status = %q, want expired", got)
	}
}

// flakyOnceCP fails the first SetSubscriptionPeriod (create has already succeeded),
// simulating a transient activation failure mid-settle.
type flakyOnceCP struct {
	*controlplane.Fake
	setCalls int
}

func (c *flakyOnceCP) SetSubscriptionPeriod(ctx context.Context, subID string, status contracts.SubscriptionStatus, expiresAt time.Time) (contracts.Subscription, error) {
	c.setCalls++
	if c.setCalls == 1 {
		return contracts.Subscription{}, fmt.Errorf("simulated transient set-period failure")
	}
	return c.Fake.SetSubscriptionPeriod(ctx, subID, status, expiresAt)
}

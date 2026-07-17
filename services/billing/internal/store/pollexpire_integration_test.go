package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/store"
)

const onchainProvider = "onchain"

var onchainSet = []string{onchainProvider}

func seedPending(t *testing.T, repo *store.Postgres, id, provider string, expiresAt time.Time) {
	t.Helper()
	if err := repo.CreateInvoice(context.Background(), model.Invoice{
		ID: id, Provider: provider, AnonUserID: "acct", Plan: "basic", Currency: "BTC",
		Amount: "0.0001", PayAddress: "addr-" + id, ProviderInvoiceID: "prov-" + id,
		Status: model.StatusPending, CreatedAt: expiresAt.Add(-time.Hour), ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func statusOf(t *testing.T, repo *store.Postgres, id string) model.Status {
	t.Helper()
	inv, err := repo.GetInvoice(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return inv.Status
}

// Effective deadline: on-chain gets ExpiresAt+grace AND needs a post-deadline negative
// check; webhook gets ExpiresAt with no grace/check.
func TestPostgres_ExpireOverdue_EffectiveDeadline(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	repo := store.NewPostgres(pool)
	ctx := context.Background()
	now := time.Now().UTC()
	grace := 15 * time.Minute

	seedPending(t, repo, "oc-in-grace", onchainProvider, now.Add(-5*time.Minute)) // deadline now+10m → not due
	seedPending(t, repo, "oc-past-nocheck", onchainProvider, now.Add(-20*time.Minute))
	seedPending(t, repo, "oc-past-check", onchainProvider, now.Add(-20*time.Minute)) // deadline now-5m
	if err := repo.RecordNegativeCheck(ctx, "oc-past-check", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	seedPending(t, repo, "wh-past", "mock", now.Add(-time.Minute))
	seedPending(t, repo, "wh-in", "mock", now.Add(time.Hour))

	if err := repo.ExpireOverdue(ctx, now, onchainSet, grace); err != nil {
		t.Fatalf("expire: %v", err)
	}

	for id, want := range map[string]model.Status{
		"oc-in-grace":     model.StatusPending, // within grace
		"oc-past-nocheck": model.StatusPending, // past grace but no post-deadline negative check
		"oc-past-check":   model.StatusExpired, // past grace + negative check
		"wh-past":         model.StatusExpired, // webhook, no grace
		"wh-in":           model.StatusPending,
	} {
		if got := statusOf(t, repo, id); got != want {
			t.Errorf("%s status = %q, want %q", id, got, want)
		}
	}
}

// The headline race: instance B sweeps right after the deadline before A has recorded
// a negative check — the invoice must NOT expire; only after a stored post-deadline
// negative check may it expire; a positive (settlement claim) never expires.
func TestPostgres_MainRace_SweepGatedByNegativeCheck(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	repo := store.NewPostgres(pool)
	ctx := context.Background()
	now := time.Now().UTC()
	grace := 15 * time.Minute

	seedPending(t, repo, "race-neg", onchainProvider, now.Add(-20*time.Minute)) // deadline now-5m
	// B sweeps immediately after the deadline; A has not checked yet → not expired.
	if err := repo.ExpireOverdue(ctx, now, onchainSet, grace); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, repo, "race-neg"); got != model.StatusPending {
		t.Fatalf("swept without a negative check: got %q, want pending", got)
	}
	// A records a definitive post-deadline negative check → the invoice may now expire.
	if err := repo.RecordNegativeCheck(ctx, "race-neg", now); err != nil {
		t.Fatal(err)
	}
	if err := repo.ExpireOverdue(ctx, now, onchainSet, grace); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, repo, "race-neg"); got != model.StatusExpired {
		t.Fatalf("not expired after a post-deadline negative check: got %q", got)
	}

	// Positive path: a settlement claim protects the invoice from expiry forever.
	seedPending(t, repo, "race-pos", onchainProvider, now.Add(-20*time.Minute))
	if ok, err := repo.ClaimSettlement(ctx, "race-pos"); err != nil || !ok {
		t.Fatalf("claim: %v ok=%v", err, ok)
	}
	if err := repo.ExpireOverdue(ctx, now, onchainSet, grace); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, repo, "race-pos"); got != model.StatusPending {
		t.Fatalf("claimed invoice expired: got %q, want pending (settles, never buried)", got)
	}
}

// A live poll lease protects an on-chain invoice from a concurrent sweep even past the
// deadline with a negative check; releasing the lease lets the sweep proceed.
func TestPostgres_ExpireOverdue_LiveLeaseProtects(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	repo := store.NewPostgres(pool)
	ctx := context.Background()
	now := time.Now().UTC()
	grace := 15 * time.Minute

	seedPending(t, repo, "leased", onchainProvider, now.Add(-20*time.Minute))
	if err := repo.RecordNegativeCheck(ctx, "leased", now); err != nil {
		t.Fatal(err)
	}
	token, acquired, err := repo.AcquirePollLease(ctx, "leased", "A", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: %v acquired=%v", err, acquired)
	}
	if err := repo.ExpireOverdue(ctx, now, onchainSet, grace); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, repo, "leased"); got != model.StatusPending {
		t.Fatalf("expired while a live lease was held: got %q", got)
	}
	if err := repo.ReleasePollLease(ctx, "leased", token); err != nil {
		t.Fatal(err)
	}
	if err := repo.ExpireOverdue(ctx, now, onchainSet, grace); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, repo, "leased"); got != model.StatusExpired {
		t.Fatalf("not expired after lease release: got %q", got)
	}
}

// Poll lease: atomic acquire, reclaim only when lapsed, and CAS renew/release so a
// stale owner can't touch a lease reclaimed by someone else.
func TestPostgres_PollLease_AcquireReclaimCAS(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	repo := store.NewPostgres(pool)
	ctx := context.Background()
	seedPending(t, repo, "pl", onchainProvider, time.Now().Add(time.Hour))

	t1, ok, err := repo.AcquirePollLease(ctx, "pl", "A", time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire A: %v ok=%v", err, ok)
	}
	// A live lease can't be re-acquired.
	if _, ok, _ := repo.AcquirePollLease(ctx, "pl", "B", time.Minute); ok {
		t.Fatal("acquired a live lease held by A")
	}
	// Renew with a stale/wrong token fails; with the real token succeeds.
	if ok, _ := repo.RenewPollLease(ctx, "pl", "wrong-token", time.Minute); ok {
		t.Fatal("renewed with a wrong token")
	}
	if ok, _ := repo.RenewPollLease(ctx, "pl", t1, time.Minute); !ok {
		t.Fatal("owner failed to renew")
	}
	// Force the lease to lapse, then B reclaims with a fresh token; A's old token is stale.
	if _, err := pool.Exec(ctx, `UPDATE poll_leases SET lease_until = now() - interval '1 minute' WHERE invoice_id = 'pl'`); err != nil {
		t.Fatal(err)
	}
	t2, ok, err := repo.AcquirePollLease(ctx, "pl", "B", time.Minute)
	if err != nil || !ok || t2 == t1 {
		t.Fatalf("reclaim B: token=%q ok=%v err=%v (must be a fresh token)", t2, ok, err)
	}
	// A's stale token can neither renew nor release B's lease.
	if ok, _ := repo.RenewPollLease(ctx, "pl", t1, time.Minute); ok {
		t.Fatal("stale owner renewed the reclaimed lease")
	}
	if err := repo.ReleasePollLease(ctx, "pl", t1); err != nil {
		t.Fatal(err)
	}
	if ok, _ := repo.RenewPollLease(ctx, "pl", t2, time.Minute); !ok {
		t.Fatal("B's lease was dropped by A's stale release")
	}
}

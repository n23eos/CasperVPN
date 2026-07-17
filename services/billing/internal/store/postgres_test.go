package store_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/store"
)

// newTestPool connects to DATABASE_URL, or skips the test when it is unset so the
// unit suite stays green offline. The schema (internal/store/schema.sql) must be
// applied to the target database out of band before running.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		// In the integration job REQUIRE_INTEGRATION_DB=true makes a missing DB a hard
		// failure — so a silently-skipped integration suite can never pass as green.
		if os.Getenv("REQUIRE_INTEGRATION_DB") == "true" {
			t.Fatal("REQUIRE_INTEGRATION_DB=true but DATABASE_URL is empty — the integration DB is mandatory here")
		}
		t.Skip("DATABASE_URL not set; skipping Postgres integration test")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	// Isolate each test: several store queries (LeaseStuckSettlements, ExpireOverdue)
	// are global scans, so leftover rows from another test — or a previous run against
	// the same DB — would pollute results and make the suite order-dependent. Start
	// every test from a clean slate.
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE settlements, invoices, seen_events, schedules RESTART IDENTITY CASCADE`); err != nil {
		pool.Close()
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

// TestPostgres_InvoiceSurvivesRestart is the core durability guarantee: an invoice
// written by one store instance is readable by a fresh instance on the same pool —
// i.e. it outlives the process, unlike the in-memory store.
func TestPostgres_InvoiceSurvivesRestart(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	id := "inv-pg-" + time.Now().UTC().Format("20060102150405.000000000")
	inv := model.Invoice{
		ID:                id,
		Provider:          "btcpay",
		AnonUserID:        "acct-pg-1",
		Plan:              "basic",
		Currency:          "BTC",
		Amount:            "0.0001",
		PayAddress:        "https://checkout/" + id,
		ProviderInvoiceID: "prov-" + id,
		Status:            model.StatusPending,
		CreatedAt:         time.Now().UTC().Truncate(time.Millisecond),
		ExpiresAt:         time.Now().UTC().Add(30 * time.Minute).Truncate(time.Millisecond),
	}

	// Write with one store instance.
	writer := store.NewPostgres(pool)
	if err := writer.CreateInvoice(ctx, inv); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, id)
	})

	// "Restart": a brand-new store instance (fresh Go object) reads it back.
	reader := store.NewPostgres(pool)
	got, err := reader.GetInvoice(ctx, id)
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if got.ID != inv.ID || got.AnonUserID != inv.AnonUserID || got.Amount != inv.Amount {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, inv)
	}
	if got.Status != model.StatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}

	// A missing id maps to ErrNotFound.
	if _, err := reader.GetInvoice(ctx, "does-not-exist"); err != store.ErrNotFound {
		t.Fatalf("missing invoice err = %v, want ErrNotFound", err)
	}
}

// TestPostgres_ClaimSettlementAtMostOnce proves the credit latch: the first claim
// wins, a second returns false, and a release re-opens it — the at-most-once
// crediting invariant that keeps double webhooks from double-crediting.
func TestPostgres_ClaimSettlementAtMostOnce(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	repo := store.NewPostgres(pool)
	invID := "claim-pg-" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM settlements WHERE invoice_id = $1`, invID)
	})

	first, err := repo.ClaimSettlement(ctx, invID)
	if err != nil || !first {
		t.Fatalf("first claim = %v, %v; want true, nil", first, err)
	}
	second, err := repo.ClaimSettlement(ctx, invID)
	if err != nil || second {
		t.Fatalf("second claim = %v, %v; want false, nil", second, err)
	}
	if err := repo.ReleaseSettlement(ctx, invID); err != nil {
		t.Fatalf("release: %v", err)
	}
	again, err := repo.ClaimSettlement(ctx, invID)
	if err != nil || !again {
		t.Fatalf("claim after release = %v, %v; want true, nil", again, err)
	}
}

// seedPGInvoice inserts a pending invoice and registers cleanup of both it and any
// settlement row keyed by it.
func seedPGInvoice(t *testing.T, pool *pgxpool.Pool, repo *store.Postgres, id string, expiresAt time.Time) {
	t.Helper()
	ctx := context.Background()
	inv := model.Invoice{
		ID: id, Provider: "mock", AnonUserID: "acct-pg", Plan: "basic",
		Currency: "BTC", Amount: "0.0001", PayAddress: "addr", ProviderInvoiceID: "prov",
		Status: model.StatusPending, CreatedAt: time.Now().UTC(), ExpiresAt: expiresAt,
	}
	if err := repo.CreateInvoice(ctx, inv); err != nil {
		t.Fatalf("seed invoice %s: %v", id, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM settlements WHERE invoice_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, id)
	})
}

// TestPostgres_LeaseRecoversStuckSettlement covers the crash-recovery path: a claimed
// but unsettled invoice is leased once, the lease is exclusive (a second immediate
// lease sees nothing), and the activation marker flows back through the lease.
func TestPostgres_LeaseRecoversStuckSettlement(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := store.NewPostgres(pool)
	id := "stuck-pg-" + time.Now().UTC().Format("20060102150405.000000000")
	seedPGInvoice(t, pool, repo, id, time.Now().Add(time.Hour))

	if ok, err := repo.ClaimSettlement(ctx, id); err != nil || !ok {
		t.Fatalf("claim: %v ok=%v", err, ok)
	}

	// Cutoff in the future so the just-made claim is eligible.
	leased, err := repo.LeaseStuckSettlements(ctx, time.Now().Add(time.Minute), 30*time.Second, 10)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if len(leased) != 1 || leased[0].InvoiceID != id || leased[0].Activated {
		t.Fatalf("lease = %+v, want [{%s false}]", leased, id)
	}

	// The lease is exclusive: a second immediate pass sees nothing (row leased).
	again, err := repo.LeaseStuckSettlements(ctx, time.Now().Add(time.Minute), 30*time.Second, 10)
	if err != nil {
		t.Fatalf("lease 2: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second lease = %+v, want empty (exclusive)", again)
	}

	// After activation and lease expiry, recovery sees Activated=true.
	if err := repo.MarkSettlementActivated(ctx, id); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE settlements SET reconcile_leased_until = now() - interval '1 minute' WHERE invoice_id = $1`, id); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	released, err := repo.LeaseStuckSettlements(ctx, time.Now().Add(time.Minute), 30*time.Second, 10)
	if err != nil {
		t.Fatalf("re-lease: %v", err)
	}
	if len(released) != 1 || !released[0].Activated {
		t.Fatalf("re-lease = %+v, want [{%s true}]", released, id)
	}
}

// TestPostgres_LeaseAgeGate: a claim younger than the cutoff is not leased.
func TestPostgres_LeaseAgeGate(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := store.NewPostgres(pool)
	id := "agegate-pg-" + time.Now().UTC().Format("20060102150405.000000000")
	seedPGInvoice(t, pool, repo, id, time.Now().Add(time.Hour))
	if ok, err := repo.ClaimSettlement(ctx, id); err != nil || !ok {
		t.Fatalf("claim: %v ok=%v", err, ok)
	}

	early, err := repo.LeaseStuckSettlements(ctx, time.Now().Add(-time.Hour), 30*time.Second, 10)
	if err != nil {
		t.Fatalf("lease early: %v", err)
	}
	for _, s := range early {
		if s.InvoiceID == id {
			t.Fatalf("claim leased below cutoff: %+v", s)
		}
	}
}

// TestPostgres_ExpireOverdueSkipsClaimed: an overdue invoice with a claim is never expired.
func TestPostgres_ExpireOverdueSkipsClaimed(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := store.NewPostgres(pool)
	stamp := time.Now().UTC().Format("20060102150405.000000000")
	claimed := "ovd-claimed-" + stamp
	open := "ovd-open-" + stamp
	past := time.Now().Add(-time.Hour)
	seedPGInvoice(t, pool, repo, claimed, past)
	seedPGInvoice(t, pool, repo, open, past)
	if ok, err := repo.ClaimSettlement(ctx, claimed); err != nil || !ok {
		t.Fatalf("claim: %v ok=%v", err, ok)
	}

	if err := repo.ExpireOverdue(ctx, time.Now()); err != nil {
		t.Fatalf("expire: %v", err)
	}

	ci, _ := repo.GetInvoice(ctx, claimed)
	if ci.Status != model.StatusPending {
		t.Fatalf("claimed invoice = %q, want still pending", ci.Status)
	}
	oi, _ := repo.GetInvoice(ctx, open)
	if oi.Status != model.StatusExpired {
		t.Fatalf("unclaimed overdue = %q, want expired", oi.Status)
	}
}

// TestPostgres_LeaseConcurrentExclusive: two reconcilers racing lease the same stuck
// invoice at most once between them (FOR UPDATE SKIP LOCKED + lease column).
func TestPostgres_LeaseConcurrentExclusive(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	ctx := context.Background()
	repo := store.NewPostgres(pool)
	id := "race-pg-" + time.Now().UTC().Format("20060102150405.000000000")
	seedPGInvoice(t, pool, repo, id, time.Now().Add(time.Hour))
	if ok, err := repo.ClaimSettlement(ctx, id); err != nil || !ok {
		t.Fatalf("claim: %v ok=%v", err, ok)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leased, err := repo.LeaseStuckSettlements(ctx, time.Now().Add(time.Minute), 30*time.Second, 10)
			if err != nil {
				return
			}
			mu.Lock()
			for _, s := range leased {
				if s.InvoiceID == id {
					total++
				}
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 1 {
		t.Fatalf("invoice leased %d times across concurrent reconcilers, want 1", total)
	}
}

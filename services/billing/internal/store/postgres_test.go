package store_test

import (
	"context"
	"os"
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

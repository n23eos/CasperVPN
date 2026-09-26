package store_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/store"
)

// The per-user advisory lock serializes same-user holders: no two run the critical
// section at once.
func TestPostgres_WithUserLock_SerializesSameUser(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	repo := store.NewPostgres(pool)
	ctx := context.Background()

	var mu sync.Mutex
	inside, overlap := false, false
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = repo.WithUserLock(ctx, "lock-user-A", func(context.Context) error {
				mu.Lock()
				if inside {
					overlap = true
				}
				inside = true
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				inside = false
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()
	if overlap {
		t.Fatal("two holders of the same user lock ran concurrently")
	}
}

// After WithUserLock returns, the lock is released so an immediate re-acquire does
// not block (proves the deferred unlock ran and the connection is reusable).
func TestPostgres_WithUserLock_ReleasesForReacquire(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	repo := store.NewPostgres(pool)
	ctx := context.Background()

	if err := repo.WithUserLock(ctx, "lock-user-B", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("first: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- repo.WithUserLock(ctx, "lock-user-B", func(context.Context) error { return nil }) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("re-acquire: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("re-acquire blocked — lock was not released")
	}
}

// A lock on one user must not block another user's lock.
func TestPostgres_WithUserLock_DifferentUsersDontBlock(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	repo := store.NewPostgres(pool)
	ctx := context.Background()

	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = repo.WithUserLock(ctx, "lock-user-C", func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	done := make(chan error, 1)
	go func() { done <- repo.WithUserLock(ctx, "lock-user-D", func(context.Context) error { return nil }) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("different-user lock: %v", err)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("a different user's lock was blocked")
	}
	close(release)
}

// Many WithUserLock cycles must complete without exhausting the pool — proving every
// acquired connection is released (no leak on the normal path).
func TestPostgres_WithUserLock_NoConnLeak(t *testing.T) {
	pool := newTestPool(t)
	defer pool.Close()
	repo := store.NewPostgres(pool)
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		if err := repo.WithUserLock(ctx, fmt.Sprintf("leak-user-%d", i), func(context.Context) error { return nil }); err != nil {
			t.Fatalf("iteration %d: %v (connection leak would exhaust the pool)", i, err)
		}
	}
}

func TestPostgres_WithUserLock_UsesPinnedConnectionWithSameUserWaiter(t *testing.T) {
	pool := newTestPoolWithMaxConns(t, 2)
	defer pool.Close()
	repo := store.NewPostgres(pool)
	seedSchedule(t, repo, "sub-pinned-two")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- repo.WithUserLock(ctx, "same-user", func(lockCtx context.Context) error {
			close(entered)
			<-proceed
			_, err := repo.GetSchedule(lockCtx, "sub-pinned-two")
			return err
		})
	}()
	<-entered
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- repo.WithUserLock(ctx, "same-user", func(context.Context) error { return nil })
	}()
	waitForAcquiredConns(t, pool, 2)
	close(proceed)
	if err := <-firstDone; err != nil {
		t.Fatalf("first holder repository call: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("same-user waiter: %v", err)
	}
}

func TestPostgres_WithUserLock_UsesPinnedConnectionWithMaxConnsOne(t *testing.T) {
	pool := newTestPoolWithMaxConns(t, 1)
	defer pool.Close()
	repo := store.NewPostgres(pool)
	now := time.Now().UTC()
	seedPGInvoice(t, pool, repo, "invoice-pinned-one", now.Add(time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := repo.WithUserLock(ctx, "one-user", func(lockCtx context.Context) error {
		credit, err := repo.StageInvoiceCredit(lockCtx, "invoice-pinned-one", "sub-pinned-one", "acct-pg", now, 30*24*time.Hour, 3*24*time.Hour)
		if err != nil {
			return err
		}
		sched, err := repo.GetSchedule(lockCtx, "sub-pinned-one")
		if err != nil {
			return err
		}
		if err := repo.UpsertSchedule(lockCtx, sched); err != nil {
			return err
		}
		expiry, staged, err := repo.StageScheduleTransition(lockCtx, "sub-pinned-one", sched.Revision, "expired", now)
		if err != nil || !staged {
			return fmt.Errorf("stage transition: staged=%t err=%v", staged, err)
		}
		if err := repo.CompleteBillingDelivery(lockCtx, credit.ID); err != nil {
			return err
		}
		return repo.CompleteBillingDelivery(lockCtx, expiry.ID)
	}); err != nil {
		t.Fatalf("repository call under one-connection lock: %v", err)
	}
}

func newTestPoolWithMaxConns(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		if os.Getenv("REQUIRE_INTEGRATION_DB") == "true" {
			t.Fatal("REQUIRE_INTEGRATION_DB=true but DATABASE_URL is empty")
		}
		t.Skip("DATABASE_URL not set; skipping Postgres integration test")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE billing_deliveries, invoice_intents, settlements, invoices, seen_events, schedules RESTART IDENTITY CASCADE`); err != nil {
		pool.Close()
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

func seedSchedule(t *testing.T, repo *store.Postgres, subID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := repo.UpsertSchedule(context.Background(), model.Schedule{
		SubID: subID, AnonUserID: "same-user", Plan: "basic", Status: "active",
		ExpiresAt: now.Add(time.Hour), GraceUntil: now.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
}

func waitForAcquiredConns(t *testing.T, pool *pgxpool.Pool, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if pool.Stat().AcquiredConns() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("acquired connections = %d, want %d", pool.Stat().AcquiredConns(), want)
}

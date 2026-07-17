package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

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

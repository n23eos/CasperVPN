//go:build integration

// Integration coverage for the durable rebuild-job store. Runs only under
// `-tags integration` against TEST_DATABASE_URL (see integration_test.go).
package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/control-plane/internal/adapters/postgres"
	"github.com/caspervpn/control-plane/internal/adapters/rebuild"
)

func TestIntegration_RebuildJobStore_ClaimLeaseCompleteRetry(t *testing.T) {
	pool := testPool(t)
	store := postgres.NewRebuildJobStore(pool)
	ctx := context.Background()

	if err := store.Enqueue(ctx, rebuild.JobKindUser, "u1"); err != nil {
		t.Fatalf("enqueue user: %v", err)
	}
	if err := store.Enqueue(ctx, rebuild.JobKindNode, "n1"); err != nil {
		t.Fatalf("enqueue node: %v", err)
	}

	// Claim both; each comes back with attempts incremented to 1.
	jobs, err := store.Claim(ctx, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("claimed %d jobs, want 2", len(jobs))
	}
	byTarget := map[string]rebuild.Job{}
	for _, j := range jobs {
		if j.Attempts != 1 {
			t.Fatalf("job %v attempts = %d, want 1", j, j.Attempts)
		}
		byTarget[j.TargetID] = j
	}
	if byTarget["u1"].Kind != rebuild.JobKindUser || byTarget["n1"].Kind != rebuild.JobKindNode {
		t.Fatalf("kinds not round-tripped: %+v", byTarget)
	}

	// Leased jobs are not re-claimable within the lease window.
	again, err := store.Claim(ctx, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim (leased): %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("leased jobs should not be re-claimed, got %d", len(again))
	}

	// Complete the user job; it is gone for good.
	if err := store.Complete(ctx, byTarget["u1"].ID); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Retry the node job with zero backoff -> immediately claimable again, and its
	// attempts keep climbing.
	if err := store.Retry(ctx, byTarget["n1"].ID, 0); err != nil {
		t.Fatalf("retry: %v", err)
	}
	retried, err := store.Claim(ctx, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim (after retry): %v", err)
	}
	if len(retried) != 1 || retried[0].TargetID != "n1" || retried[0].Attempts != 2 {
		t.Fatalf("expected n1 reclaimed with attempts=2, got %+v", retried)
	}

	// Completing it drains the queue.
	if err := store.Complete(ctx, retried[0].ID); err != nil {
		t.Fatalf("complete node: %v", err)
	}
	empty, err := store.Claim(ctx, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim (empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("queue should be empty, got %d", len(empty))
	}
}

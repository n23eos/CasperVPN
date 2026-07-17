package store

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/model"
)

func memSeed(t *testing.T, m *Memory, id, provider string, expiresAt time.Time) {
	t.Helper()
	if err := m.CreateInvoice(context.Background(), model.Invoice{
		ID: id, Provider: provider, Status: model.StatusPending, ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatal(err)
	}
}

// Memory mirrors the Postgres effective-deadline + negative-check + lease/claim gating.
func TestMemory_ExpireOverdue_EffectiveDeadlineParity(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := NewMemoryWithClock(func() time.Time { return now })
	ctx := context.Background()
	grace := 15 * time.Minute
	onchain := []string{"onchain"}

	memSeed(t, m, "oc-in-grace", "onchain", now.Add(-5*time.Minute))
	memSeed(t, m, "oc-past-nocheck", "onchain", now.Add(-20*time.Minute))
	memSeed(t, m, "oc-past-check", "onchain", now.Add(-20*time.Minute))
	_ = m.RecordNegativeCheck(ctx, "oc-past-check", now.Add(-time.Minute))
	memSeed(t, m, "wh-past", "mock", now.Add(-time.Minute))

	// A live lease protects an otherwise-expirable on-chain invoice.
	memSeed(t, m, "oc-leased", "onchain", now.Add(-20*time.Minute))
	_ = m.RecordNegativeCheck(ctx, "oc-leased", now.Add(-time.Minute))
	if _, ok, _ := m.AcquirePollLease(ctx, "oc-leased", "A", time.Minute); !ok {
		t.Fatal("acquire")
	}

	if err := m.ExpireOverdue(ctx, now, onchain, grace); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]model.Status{
		"oc-in-grace":     model.StatusPending,
		"oc-past-nocheck": model.StatusPending,
		"oc-past-check":   model.StatusExpired,
		"wh-past":         model.StatusExpired,
		"oc-leased":       model.StatusPending, // live lease
	} {
		inv, _ := m.GetInvoice(ctx, id)
		if inv.Status != want {
			t.Errorf("%s = %q, want %q", id, inv.Status, want)
		}
	}
}

func TestMemory_PollLease_CAS(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := now
	m := NewMemoryWithClock(func() time.Time { return clk })
	ctx := context.Background()

	t1, ok, _ := m.AcquirePollLease(ctx, "pl", "A", time.Minute)
	if !ok {
		t.Fatal("acquire A")
	}
	if _, ok, _ := m.AcquirePollLease(ctx, "pl", "B", time.Minute); ok {
		t.Fatal("acquired a live lease")
	}
	if ok, _ := m.RenewPollLease(ctx, "pl", "wrong", time.Minute); ok {
		t.Fatal("renewed with wrong token")
	}
	// Advance past the lease so B can reclaim with a fresh token.
	clk = now.Add(2 * time.Minute)
	t2, ok, _ := m.AcquirePollLease(ctx, "pl", "B", time.Minute)
	if !ok || t2 == t1 {
		t.Fatalf("reclaim: token=%q ok=%v (want fresh)", t2, ok)
	}
	if ok, _ := m.RenewPollLease(ctx, "pl", t1, time.Minute); ok {
		t.Fatal("stale token renewed reclaimed lease")
	}
	_ = m.ReleasePollLease(ctx, "pl", t1) // stale release is a no-op
	if ok, _ := m.RenewPollLease(ctx, "pl", t2, time.Minute); !ok {
		t.Fatal("B's lease dropped by stale release")
	}
}

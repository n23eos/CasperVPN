package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/store"
)

func TestStageInvoiceCreditFixesTargetAndAdvancesOncePerInvoice(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := store.NewMemoryWithClock(func() time.Time { return now })
	ctx := context.Background()
	for _, id := range []string{"inv-1", "inv-2"} {
		if err := repo.CreateInvoice(ctx, model.Invoice{ID: id, AnonUserID: "acct-1", Plan: "basic", Status: model.StatusPending}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := repo.StageInvoiceCredit(ctx, "inv-1", "sub-1", "acct-1", now, 30*24*time.Hour, 3*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repo.StageInvoiceCredit(ctx, "inv-1", "sub-1", "acct-1", now.Add(time.Hour), 30*24*time.Hour, 3*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != first.ID || replay.Revision != 1 || !replay.ExpiresAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("replay changed target: first=%+v replay=%+v", first, replay)
	}
	second, err := repo.StageInvoiceCredit(ctx, "inv-2", "sub-1", "acct-1", now, 30*24*time.Hour, 3*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 || !second.ExpiresAt.Equal(now.Add(60*24*time.Hour)) {
		t.Fatalf("second invoice target = rev %d expiry %v, want rev 2 and 60 days", second.Revision, second.ExpiresAt)
	}
}

func TestStageScheduleTransitionRejectsStaleRevisionAfterRenewal(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := store.NewMemoryWithClock(func() time.Time { return now })
	ctx := context.Background()
	_ = repo.CreateInvoice(ctx, model.Invoice{ID: "inv-1", AnonUserID: "acct-1", Plan: "basic", Status: model.StatusPending})
	_, _ = repo.StageInvoiceCredit(ctx, "inv-1", "sub-1", "acct-1", now, 30*24*time.Hour, 3*24*time.Hour)
	if _, staged, err := repo.StageScheduleTransition(ctx, "sub-1", 0, "expired", now); err != nil || staged {
		t.Fatalf("stale transition staged=%t err=%v, want false,nil", staged, err)
	}
	sched, err := repo.GetSchedule(ctx, "sub-1")
	if err != nil || sched.Status != "active" || sched.Revision != 1 {
		t.Fatalf("schedule changed by stale sweep: %+v err=%v", sched, err)
	}
}

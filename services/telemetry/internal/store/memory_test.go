package store

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

func mkSignal(id string, at time.Time) contracts.FieldSignal {
	return contracts.FieldSignal{
		SignalID: id, NodeID: "n1", TransportType: contracts.TransportVlessReality,
		Region: "RU-MOW", SignalType: contracts.SignalOK, ObservedAt: at,
	}
}

func TestMemoryStore_WriteAndSince(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	s := NewMemoryStore(0)

	_ = s.WriteSignals(ctx, []contracts.FieldSignal{
		mkSignal("a", base.Add(-2*time.Hour)),
		mkSignal("b", base.Add(-10*time.Minute)),
		mkSignal("c", base.Add(-1*time.Minute)),
	})

	got, err := s.SignalsSince(ctx, base.Add(-15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("SignalsSince returned %d, want 2 (b,c)", len(got))
	}
	// Sorted oldest-first.
	if !got[0].ObservedAt.Before(got[1].ObservedAt) {
		t.Fatal("signals not sorted ascending")
	}
}

func TestMemoryStore_Prune(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	s := NewMemoryStore(0)
	_ = s.WriteSignals(ctx, []contracts.FieldSignal{
		mkSignal("old", base.Add(-100*time.Hour)),
		mkSignal("new", base.Add(-1*time.Minute)),
	})

	removed, err := s.Prune(ctx, base.Add(-72*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("pruned %d, want 1", removed)
	}
	got, _ := s.SignalsSince(ctx, time.Time{})
	if len(got) != 1 || got[0].SignalID != "new" {
		t.Fatalf("after prune want only 'new', got %+v", got)
	}
}

func TestMemoryStore_HardCapDropsOldest(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	s := NewMemoryStore(2)
	_ = s.WriteSignals(ctx, []contracts.FieldSignal{mkSignal("a", base.Add(-3*time.Minute))})
	_ = s.WriteSignals(ctx, []contracts.FieldSignal{mkSignal("b", base.Add(-2*time.Minute))})
	_ = s.WriteSignals(ctx, []contracts.FieldSignal{mkSignal("c", base.Add(-1*time.Minute))})

	got, _ := s.SignalsSince(ctx, time.Time{})
	if len(got) != 2 {
		t.Fatalf("hard cap should keep 2, got %d", len(got))
	}
	if got[0].SignalID != "b" || got[1].SignalID != "c" {
		t.Fatalf("hard cap should keep newest (b,c), got %s,%s", got[0].SignalID, got[1].SignalID)
	}
}

//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/postgres"
	"github.com/caspervpn/control-plane/internal/domain"
)

func TestIntegration_DemoteConditionalTransition(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	nodes := postgres.NewNodeStore(pool)

	// active -> provisioning (allowed)
	if err := nodes.Create(ctx, activeNode("d1")); err != nil {
		t.Fatal(err)
	}
	if prev, err := nodes.Demote(ctx, "d1"); err != nil || prev != contracts.NodeStatusActive {
		t.Fatalf("demote active: prev=%s err=%v", prev, err)
	}
	if n, _ := nodes.Get(ctx, "d1"); n.Status != contracts.NodeStatusProvisioning {
		t.Fatalf("d1 status=%s, want provisioning", n.Status)
	}
	// idempotent: provisioning -> provisioning
	if _, err := nodes.Demote(ctx, "d1"); err != nil {
		t.Fatalf("demote provisioning should be ok: %v", err)
	}

	// retired -> ErrConflict (terminal, protected)
	rn := activeNode("d2")
	rn.Status = contracts.NodeStatusRetired
	if err := nodes.Create(ctx, rn); err != nil {
		t.Fatal(err)
	}
	if _, err := nodes.Demote(ctx, "d2"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("demote retired: got %v, want ErrConflict", err)
	}
	// draining -> ErrConflict
	dn := activeNode("d3")
	dn.Status = contracts.NodeStatusDraining
	if err := nodes.Create(ctx, dn); err != nil {
		t.Fatal(err)
	}
	if _, err := nodes.Demote(ctx, "d3"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("demote draining: got %v, want ErrConflict", err)
	}
	// missing -> ErrNotFound
	if _, err := nodes.Demote(ctx, "ghost"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("demote missing: got %v, want ErrNotFound", err)
	}
}

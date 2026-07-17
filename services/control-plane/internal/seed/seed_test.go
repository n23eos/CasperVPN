package seed

import (
	"context"
	"testing"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/memory"
	"github.com/caspervpn/control-plane/internal/usecase"
)

// The dev seeder must bring the fleet up SERVING. Register refuses status:active,
// so this guards that the seeder still produces active nodes (via the repo seam)
// and that a re-run is a no-op — otherwise `make up` would silently start empty.
func TestRun_SeedsActiveFleetIdempotently(t *testing.T) {
	nodes := memory.NewNodes()
	users := memory.NewUsers()
	subs := memory.NewSubscriptions()
	rot := memory.NewRotations()
	sets := memory.NewSets()
	q := memory.NewNoopQueue()

	svc := Services{
		Nodes:     usecase.NewNodeService(nodes, rot, q),
		Users:     usecase.NewUserService(users, rot, q),
		Subs:      usecase.NewSubscriptionService(subs, users),
		Bundles:   usecase.NewBundleService(users, nodes, sets),
		NodesRepo: nodes,
	}
	ctx := context.Background()

	if err := Run(ctx, svc); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	active, err := nodes.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active nodes = %d, want 2 (dev fleet must come up serving)", len(active))
	}
	for _, n := range active {
		if n.Status != contracts.NodeStatusActive {
			t.Fatalf("seeded node %s status = %q, want active", n.ID, n.Status)
		}
	}

	// Idempotent: a second run sees a non-empty fleet and does nothing.
	if err := Run(ctx, svc); err != nil {
		t.Fatalf("second seed run: %v", err)
	}
	if again, _ := nodes.ListActive(ctx); len(again) != 2 {
		t.Fatalf("after re-run active = %d, want 2 (idempotent)", len(again))
	}
}

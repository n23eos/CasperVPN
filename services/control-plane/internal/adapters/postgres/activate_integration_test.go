//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/postgres"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/usecase"
)

func provEntry(id, exitID string) contracts.Node {
	return contracts.Node{
		ID: id, Role: contracts.NodeRoleEntry, Status: contracts.NodeStatusProvisioning,
		Region: "eu", EntryIP: "10.9.0.1", CreatedAt: time.Now().UTC(),
		Transports: []contracts.Transport{
			{Tag: "v", Type: contracts.TransportVlessReality, Version: contracts.TransportVersionV1, Port: 443, Enabled: true,
				VlessReality: &contracts.VlessRealityParams{ServerNames: []string{"a.com"}, Dest: "a.com:443", PublicKey: "pk", ShortIDs: []string{"aa"}, Flow: "v"}},
			{Tag: "h", Type: contracts.TransportHysteria2, Version: contracts.TransportVersionV1, Port: 8443, Enabled: true,
				Hysteria2: &contracts.Hysteria2Params{Password: "pw", SNI: "a.com"}},
		},
	}
}

func exitNode(id, entryID string, status contracts.NodeStatus) contracts.Node {
	eid := entryID
	return contracts.Node{
		ID: id, Role: contracts.NodeRoleExit, Status: status, EntryNodeID: &eid,
		Region: "eu", CreatedAt: time.Now().UTC(),
	}
}

func TestIntegration_ActivateGuardedAndSerialized(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	nodeStore := postgres.NewNodeStore(pool)
	userStore := postgres.NewUserStore(pool)
	subStore := postgres.NewSubscriptionStore(pool)
	usvc := usecase.NewUserService(userStore, postgres.NewRotationStore(pool), noQueue{})
	ssvc := usecase.NewSubscriptionService(subStore, userStore)

	// eligible user
	u, err := usvc.Create(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssvc.Create(ctx, u.ID, contracts.SubscriptionPlanBasic); err != nil {
		t.Fatal(err)
	}
	// entry first — exit.entry_node_id FKs to it.
	if err := nodeStore.Create(ctx, provEntry("en", "ex")); err != nil {
		t.Fatal(err)
	}
	if err := nodeStore.Create(ctx, exitNode("ex", "en", contracts.NodeStatusActive)); err != nil {
		t.Fatal(err)
	}

	users, err := userStore.EligibleRealityUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rev := contracts.RealityUsersRevision(users)

	// stale revision -> conflict, node stays provisioning
	if _, _, err := nodeStore.Activate(ctx, "en", "stale"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale revision: got %v, want ErrConflict", err)
	}

	// concurrent correct activations: exactly one wins, the rest 409.
	const N = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, conflict int
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := nodeStore.Activate(ctx, "en", rev)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, domain.ErrConflict):
				conflict++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || conflict != N-1 {
		t.Fatalf("concurrent activate: ok=%d conflict=%d, want 1/%d", ok, conflict, N-1)
	}

	got, err := nodeStore.Get(ctx, "en")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.NodeStatusActive {
		t.Fatalf("final status = %s, want active", got.Status)
	}
}

func TestIntegration_ActivateRejectsIneligibleStructure(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	nodeStore := postgres.NewNodeStore(pool)

	// entry with one transport type + no exit -> conflict on structure.
	one := provEntry("en1", "ex1")
	one.Transports = one.Transports[:1] // single vless-reality
	if err := nodeStore.Create(ctx, one); err != nil {
		t.Fatal(err)
	}
	if _, _, err := nodeStore.Activate(ctx, "en1", contracts.RealityUsersRevision(nil)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("one transport type: got %v, want ErrConflict", err)
	}

	// two transports but exit provisioning -> conflict. Entry first (FK).
	if err := nodeStore.Create(ctx, provEntry("en2", "ex2")); err != nil {
		t.Fatal(err)
	}
	if err := nodeStore.Create(ctx, exitNode("ex2", "en2", contracts.NodeStatusProvisioning)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := nodeStore.Activate(ctx, "en2", contracts.RealityUsersRevision(nil)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("exit not active: got %v, want ErrConflict", err)
	}
}

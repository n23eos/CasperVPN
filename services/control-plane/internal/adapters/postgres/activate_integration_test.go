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
	if _, _, err := nodeStore.Activate(ctx, "en", "stale", ""); !errors.Is(err, domain.ErrConflict) {
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
			_, _, err := nodeStore.Activate(ctx, "en", rev, "")
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

// TestIntegration_ActivateExitThenEntrySequence walks the reconciler's ordering:
// exit activates first (role-aware, no transport/revision checks), then the entry
// (which requires its exit already active).
func TestIntegration_ActivateExitThenEntrySequence(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	nodeStore := postgres.NewNodeStore(pool)
	userStore := postgres.NewUserStore(pool)
	subStore := postgres.NewSubscriptionStore(pool)
	usvc := usecase.NewUserService(userStore, postgres.NewRotationStore(pool), noQueue{})
	ssvc := usecase.NewSubscriptionService(subStore, userStore)

	u, err := usvc.Create(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssvc.Create(ctx, u.ID, contracts.SubscriptionPlanBasic); err != nil {
		t.Fatal(err)
	}
	if err := nodeStore.Create(ctx, provEntry("es-en", "es-ex")); err != nil {
		t.Fatal(err)
	}
	if err := nodeStore.Create(ctx, exitNode("es-ex", "es-en", contracts.NodeStatusProvisioning)); err != nil {
		t.Fatal(err)
	}
	users, _ := userStore.EligibleRealityUsers(ctx)
	rev := contracts.RealityUsersRevision(users)

	// entry cannot go active while its exit is provisioning.
	if _, _, err := nodeStore.Activate(ctx, "es-en", rev, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("entry before exit: got %v, want ErrConflict", err)
	}
	// activate exit first.
	if _, _, err := nodeStore.Activate(ctx, "es-ex", "", contracts.ExitDataPlaneVerified); err != nil {
		t.Fatalf("activate exit: %v", err)
	}
	// entry now activates.
	if _, _, err := nodeStore.Activate(ctx, "es-en", rev, ""); err != nil {
		t.Fatalf("activate entry after exit: %v", err)
	}
	got, _ := nodeStore.Get(ctx, "es-en")
	if got.Status != contracts.NodeStatusActive {
		t.Fatalf("entry final status = %s, want active", got.Status)
	}
}

// TestIntegration_ActivateEligibilityRaceStaysConsistent runs activation
// concurrently with an eligibility mutation (banning the only eligible user)
// under SERIALIZABLE. The activation must never crash and never activate against
// a stale allow-list: it either commits with the revision it verified, or loses
// the race (ErrConflict). Repeated to exercise the timing window.
func TestIntegration_ActivateEligibilityRaceStaysConsistent(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	nodeStore := postgres.NewNodeStore(pool)
	userStore := postgres.NewUserStore(pool)
	subStore := postgres.NewSubscriptionStore(pool)
	usvc := usecase.NewUserService(userStore, postgres.NewRotationStore(pool), noQueue{})
	ssvc := usecase.NewSubscriptionService(subStore, userStore)

	for iter := 0; iter < 25; iter++ {
		truncate(t, pool)
		u, err := usvc.Create(ctx, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ssvc.Create(ctx, u.ID, contracts.SubscriptionPlanBasic); err != nil {
			t.Fatal(err)
		}
		entryID := "rc-en"
		if err := nodeStore.Create(ctx, provEntry(entryID, "rc-ex")); err != nil {
			t.Fatal(err)
		}
		if err := nodeStore.Create(ctx, exitNode("rc-ex", entryID, contracts.NodeStatusActive)); err != nil {
			t.Fatal(err)
		}
		users, _ := userStore.EligibleRealityUsers(ctx)
		rev := contracts.RealityUsersRevision(users)

		var wg sync.WaitGroup
		wg.Add(2)
		var actErr error
		go func() {
			defer wg.Done()
			_, _, actErr = nodeStore.Activate(ctx, entryID, rev, "")
		}()
		go func() {
			defer wg.Done()
			// ban the only eligible user -> eligibility changes
			banned := u
			banned.Status = contracts.UserStatusBanned
			_, _ = usvc.Update(ctx, u.ID, banned, "race")
		}()
		wg.Wait()

		if actErr != nil && !errors.Is(actErr, domain.ErrConflict) {
			t.Fatalf("iter %d: unexpected activate error: %v", iter, actErr)
		}
		got, _ := nodeStore.Get(ctx, entryID)
		if actErr == nil {
			// If it activated, it must have committed BEFORE the ban took effect,
			// i.e. under serializable ordering the revision it verified was valid.
			if got.Status != contracts.NodeStatusActive {
				t.Fatalf("iter %d: nil error but status=%s", iter, got.Status)
			}
		} else if got.Status != contracts.NodeStatusProvisioning {
			t.Fatalf("iter %d: conflict but status=%s (want provisioning)", iter, got.Status)
		}
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
	if _, _, err := nodeStore.Activate(ctx, "en1", contracts.RealityUsersRevision(nil), ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("one transport type: got %v, want ErrConflict", err)
	}

	// two transports but exit provisioning -> conflict. Entry first (FK).
	if err := nodeStore.Create(ctx, provEntry("en2", "ex2")); err != nil {
		t.Fatal(err)
	}
	if err := nodeStore.Create(ctx, exitNode("ex2", "en2", contracts.NodeStatusProvisioning)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := nodeStore.Activate(ctx, "en2", contracts.RealityUsersRevision(nil), ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("exit not active: got %v, want ErrConflict", err)
	}
}

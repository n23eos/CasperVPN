//go:build integration

// Integration tests exercise the real Postgres adapters end to end. They run
// only under `-tags integration` and need TEST_DATABASE_URL pointing at a
// disposable Postgres (e.g. a testcontainers/compose instance):
//
//	TEST_DATABASE_URL=postgres://caspervpn:dev_only_change_me@localhost:5432/caspervpn?sslmode=disable \
//	  go test -tags integration ./internal/adapters/postgres/...
package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/postgres"
	"github.com/caspervpn/control-plane/internal/adapters/rebuild"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/migrate"
	"github.com/caspervpn/control-plane/internal/usecase"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration tests")
	}
	ctx := context.Background()
	pool, err := postgres.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	truncate(t, pool)
	t.Cleanup(func() { truncate(t, pool); pool.Close() })
	return pool
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		TRUNCATE node_signal_aggregates, subscription_sets, user_secret_rotations,
			node_rotation_history, transports, nodes, subscriptions, users RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func TestIntegration_NodeCRUDAndTransportsRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	nodes := postgres.NewNodeStore(pool)

	n := contracts.Node{
		ID: "n1", Role: contracts.NodeRoleCombined, Status: contracts.NodeStatusActive,
		Provider: "hetzner", Region: "eu", EntryIP: "1.2.3.4", CreatedAt: time.Now().UTC(),
		Transports: []contracts.Transport{{
			Tag: "r0", Type: contracts.TransportVlessReality, Version: contracts.TransportVersionV1,
			Port: 443, Enabled: true, Priority: 0,
			VlessReality: &contracts.VlessRealityParams{
				ServerNames: []string{"a.com"}, Dest: "a.com:443", PublicKey: "pk", ShortIDs: []string{"aa"}, Flow: "v",
			},
		}},
	}
	if err := nodes.Create(ctx, n); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := nodes.Get(ctx, "n1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Transports) != 1 || got.Transports[0].VlessReality == nil {
		t.Fatalf("transport not round-tripped: %+v", got.Transports)
	}
	active, err := nodes.ListActive(ctx)
	if err != nil || len(active) != 1 {
		t.Fatalf("list active: %v len=%d", err, len(active))
	}
}

func TestIntegration_SubscriptionStoresHashOnly(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	users := postgres.NewUserStore(pool)
	subs := postgres.NewSubscriptionStore(pool)

	usvc := usecase.NewUserService(users, postgres.NewRotationStore(pool), noQueue{})
	u, err := usvc.Create(ctx, nil, nil)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	svc := usecase.NewSubscriptionService(subs, users)
	res, err := svc.Create(ctx, u.ID, contracts.SubscriptionPlanBasic)
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	got, err := svc.Get(ctx, res.Subscription.ID)
	if err != nil {
		t.Fatalf("get sub: %v", err)
	}
	if got.Token != "" {
		t.Fatal("token must never be readable")
	}
	// Confirm the raw column holds a hash, not the plaintext.
	var hash string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM subscriptions WHERE id=$1`, res.Subscription.ID).Scan(&hash); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if hash == res.PlainToken || hash == "" {
		t.Fatal("token must be stored hashed, not in the clear")
	}
}

// TestIntegration_BlockTriggersRebuild is the anti-block acceptance path against
// real Postgres: a blocked node drops out of a user's rebuilt set automatically.
func TestIntegration_BlockTriggersRebuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := testPool(t)

	nodes := postgres.NewNodeStore(pool)
	users := postgres.NewUserStore(pool)
	rot := postgres.NewRotationStore(pool)
	sets := postgres.NewSetStore(pool)
	sigs := postgres.NewSignalStore(pool)

	bundle := usecase.NewBundleService(users, nodes, sets)
	queue := rebuild.New(sets, bundle, 64, 2, nil)
	queue.Start(ctx)
	nodeSvc := usecase.NewNodeService(nodes, rot, queue)
	userSvc := usecase.NewUserService(users, rot, queue)
	sigSvc := usecase.NewSignalService(nodes, sigs, nodeSvc)

	// Two active nodes.
	for _, id := range []string{"na", "nb"} {
		if _, err := nodeSvc.Register(ctx, activeNode(id), "seed"); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	u, err := userSvc.Create(ctx, nil, nil)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	set, err := bundle.Build(ctx, u.ID)
	if err != nil || len(set.Bundle.Nodes) != 2 {
		t.Fatalf("initial build: err=%v nodes=%d", err, len(set.Bundle.Nodes))
	}

	// A field signal blocks node "na". The queue must rebuild the user's set
	// without it — no manual action.
	agg := domain.SignalAggregate{
		NodeID: "na", Region: "RU-MOW", Total: 100,
		BySignal: map[contracts.SignalType]int{contracts.SignalDPIBlock: 90, contracts.SignalOK: 10},
	}
	if _, err := sigSvc.Ingest(ctx, []domain.SignalAggregate{agg}, "telemetry"); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// Poll for the async rebuild to converge on 1 node (rev > 1).
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := sets.Get(ctx, u.ID)
		if err == nil && !got.Dirty && len(got.Bundle.Nodes) == 1 && got.Bundle.Nodes[0].ID == "nb" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rebuild did not converge: %+v (err=%v)", got, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	blocked, _ := nodes.Get(ctx, "na")
	if blocked.Status != contracts.NodeStatusBlocked {
		t.Fatalf("node na not blocked: %s", blocked.Status)
	}
}

func activeNode(id string) contracts.Node {
	return contracts.Node{
		ID: id, Role: contracts.NodeRoleCombined, Status: contracts.NodeStatusActive,
		Region: "eu", EntryIP: "10.0.0." + id, CreatedAt: time.Now().UTC(),
		Transports: []contracts.Transport{{
			Tag: "r0", Type: contracts.TransportVlessReality, Version: contracts.TransportVersionV1,
			Port: 443, Enabled: true,
			VlessReality: &contracts.VlessRealityParams{
				ServerNames: []string{"a.com"}, Dest: "a.com:443", PublicKey: "pk", ShortIDs: []string{"aa"}, Flow: "v",
			},
		}},
	}
}

// noQueue is a no-op RebuildQueue for tests that don't assert on rebuilds.
type noQueue struct{}

func (noQueue) EnqueueNode(string) {}
func (noQueue) EnqueueUser(string) {}

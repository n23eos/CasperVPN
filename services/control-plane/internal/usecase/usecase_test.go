package usecase

import (
	"context"
	"testing"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/secret"
)

func activeNode(id, entryIP string) contracts.Node {
	return contracts.Node{
		ID:      id,
		Role:    contracts.NodeRoleCombined,
		Status:  contracts.NodeStatusActive,
		Region:  "eu-central",
		EntryIP: entryIP,
		Transports: []contracts.Transport{{
			Tag: "reality-0", Type: contracts.TransportVlessReality,
			Version: contracts.TransportVersionV1, Port: 443, Enabled: true,
			VlessReality: &contracts.VlessRealityParams{
				ServerNames: []string{"example.com"}, Dest: "example.com:443",
				PublicKey: "pk", ShortIDs: []string{"aa"}, Flow: "xtls-rprx-vision",
			},
		}},
	}
}

func TestUserCreate_IssuesDistinctPersonalSecrets(t *testing.T) {
	users := newFakeUserRepo()
	svc := NewUserService(users, newFakeRotationRepo(), newFakeQueue())

	a, err := svc.Create(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := svc.Create(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if a.RealityShortID == b.RealityShortID || a.UUID == b.UUID {
		t.Fatalf("users share secrets: isolation broken")
	}
	if a.PrivateKey == "" || a.Status != contracts.UserStatusActive {
		t.Fatalf("user missing key/status: %+v", a)
	}
}

func TestSubscriptionCreate_StoresOnlyHashedToken(t *testing.T) {
	users := newFakeUserRepo()
	subs := newFakeSubRepo()
	usvc := NewUserService(users, newFakeRotationRepo(), newFakeQueue())
	u, _ := usvc.Create(context.Background(), nil, nil)

	svc := NewSubscriptionService(subs, users)
	res, err := svc.Create(context.Background(), u.ID, contracts.SubscriptionPlanBasic)
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	if res.PlainToken == "" {
		t.Fatal("expected plaintext token returned once")
	}
	if subs.subs[res.Subscription.ID].Token != "" {
		t.Fatal("plaintext token must not be persisted")
	}
	if subs.hashes[res.Subscription.ID] != secret.HashToken(res.PlainToken) {
		t.Fatal("stored hash does not match token")
	}
	got, err := svc.Get(context.Background(), res.Subscription.ID)
	if err != nil {
		t.Fatalf("get sub: %v", err)
	}
	if got.Token != "" {
		t.Fatal("Get must never expose the token")
	}
}

func TestSubscriptionCreate_UnknownUser(t *testing.T) {
	svc := NewSubscriptionService(newFakeSubRepo(), newFakeUserRepo())
	_, err := svc.Create(context.Background(), "ghost", contracts.SubscriptionPlanBasic)
	if err == nil {
		t.Fatal("expected error for unknown user")
	}
}

func TestBundleBuild_DynamicActiveNodes_PerUserIsolation(t *testing.T) {
	ctx := context.Background()
	users := newFakeUserRepo()
	nodes := newFakeNodeRepo()
	sets := newFakeSetRepo()
	usvc := NewUserService(users, newFakeRotationRepo(), newFakeQueue())
	ua, _ := usvc.Create(ctx, nil, nil)
	ub, _ := usvc.Create(ctx, nil, nil)

	_ = nodes.Create(ctx, activeNode("n1", "1.1.1.1"))
	_ = nodes.Create(ctx, activeNode("n2", "2.2.2.2"))
	blocked := activeNode("n3", "3.3.3.3")
	blocked.Status = contracts.NodeStatusBlocked
	_ = nodes.Create(ctx, blocked)

	svc := NewBundleService(users, nodes, sets)
	set, err := svc.Build(ctx, ua.ID)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(set.Bundle.Nodes) != 2 {
		t.Fatalf("expected only 2 active nodes, got %d", len(set.Bundle.Nodes))
	}
	if set.Bundle.User.RealityShortID != ua.RealityShortID {
		t.Fatal("bundle must carry the requesting user's secrets")
	}
	if set.Bundle.User.RealityShortID == ub.RealityShortID {
		t.Fatal("bundle leaked another user's secret")
	}
	if set.Revision != 1 {
		t.Fatalf("expected revision 1, got %d", set.Revision)
	}
	// Second build bumps the revision.
	set2, _ := svc.Build(ctx, ua.ID)
	if set2.Revision != 2 {
		t.Fatalf("expected revision 2, got %d", set2.Revision)
	}
}

func TestSignalIngest_BlocksNodeAndEnqueuesRebuild(t *testing.T) {
	ctx := context.Background()
	nodes := newFakeNodeRepo()
	rot := newFakeRotationRepo()
	queue := newFakeQueue()
	_ = nodes.Create(ctx, activeNode("n1", "1.1.1.1"))

	nodeSvc := NewNodeService(nodes, rot, queue)
	sigs := newFakeSignalRepo()
	svc := NewSignalService(nodes, sigs, nodeSvc)

	agg := domain.SignalAggregate{
		NodeID: "n1", Region: "RU-MOW", Total: 100,
		BySignal: map[contracts.SignalType]int{contracts.SignalDPIBlock: 80, contracts.SignalOK: 20},
	}
	changes, err := svc.Ingest(ctx, []domain.SignalAggregate{agg}, "telemetry")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(changes) != 1 || changes[0].To != contracts.NodeStatusBlocked {
		t.Fatalf("expected node blocked, got %+v", changes)
	}
	n, _ := nodes.Get(ctx, "n1")
	if n.Status != contracts.NodeStatusBlocked {
		t.Fatalf("node not persisted blocked: %s", n.Status)
	}
	if len(queue.nodes) == 0 {
		t.Fatal("blocked node must enqueue a rebuild")
	}
	if len(rot.nodeRecs) == 0 {
		t.Fatal("status change must be recorded in rotation history")
	}
}

func TestSignalIngest_DegradedOnFailuresWithoutBlockMajority(t *testing.T) {
	ctx := context.Background()
	nodes := newFakeNodeRepo()
	_ = nodes.Create(ctx, activeNode("n1", "1.1.1.1"))
	nodeSvc := NewNodeService(nodes, newFakeRotationRepo(), newFakeQueue())
	svc := NewSignalService(nodes, newFakeSignalRepo(), nodeSvc)

	// 40% throttle (failure, not block-class) => degraded, not blocked.
	agg := domain.SignalAggregate{
		NodeID: "n1", Total: 100,
		BySignal: map[contracts.SignalType]int{contracts.SignalThrottle: 40, contracts.SignalOK: 60},
	}
	changes, err := svc.Ingest(ctx, []domain.SignalAggregate{agg}, "telemetry")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(changes) != 1 || changes[0].To != contracts.NodeStatusDegraded {
		t.Fatalf("expected degraded, got %+v", changes)
	}
}

func TestSignalIngest_DoesNotAutoUnblock(t *testing.T) {
	ctx := context.Background()
	nodes := newFakeNodeRepo()
	blocked := activeNode("n1", "1.1.1.1")
	blocked.Status = contracts.NodeStatusBlocked
	_ = nodes.Create(ctx, blocked)
	nodeSvc := NewNodeService(nodes, newFakeRotationRepo(), newFakeQueue())
	svc := NewSignalService(nodes, newFakeSignalRepo(), nodeSvc)

	agg := domain.SignalAggregate{
		NodeID: "n1", Total: 100,
		BySignal: map[contracts.SignalType]int{contracts.SignalOK: 100},
	}
	changes, err := svc.Ingest(ctx, []domain.SignalAggregate{agg}, "telemetry")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("blocked node must not auto-recover, got %+v", changes)
	}
}

func TestNodeRegisterAndRetire_History(t *testing.T) {
	ctx := context.Background()
	nodes := newFakeNodeRepo()
	rot := newFakeRotationRepo()
	queue := newFakeQueue()
	svc := NewNodeService(nodes, rot, queue)

	n, err := svc.Register(ctx, contracts.Node{Role: contracts.NodeRoleCombined}, "orchestrator")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if n.ID == "" || n.Status != contracts.NodeStatusProvisioning {
		t.Fatalf("register defaults wrong: %+v", n)
	}
	if err := svc.Retire(ctx, n.ID, "orchestrator"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	got, _ := nodes.Get(ctx, n.ID)
	if got.Status != contracts.NodeStatusRetired {
		t.Fatalf("expected retired, got %s", got.Status)
	}
	if len(rot.nodeRecs) < 2 {
		t.Fatalf("expected register+retire history, got %d", len(rot.nodeRecs))
	}
	if len(queue.nodes) == 0 {
		t.Fatal("retire must enqueue rebuild")
	}
}

func TestUserRotateSecrets_ChangesSecretsAndEnqueues(t *testing.T) {
	ctx := context.Background()
	users := newFakeUserRepo()
	rot := newFakeRotationRepo()
	queue := newFakeQueue()
	svc := NewUserService(users, rot, queue)
	u, _ := svc.Create(ctx, nil, nil)
	oldSID, oldUUID := u.RealityShortID, u.UUID

	updated, err := svc.RotateSecrets(ctx, u.ID, "abuse", "admin")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if updated.RealityShortID == oldSID || updated.UUID == oldUUID {
		t.Fatal("secrets did not rotate")
	}
	if len(rot.userRecs) != 1 || rot.userRecs[0].OldRealityShortID != oldSID {
		t.Fatal("secret rotation not archived")
	}
	if len(queue.users) == 0 {
		t.Fatal("rotate must enqueue user rebuild")
	}
}

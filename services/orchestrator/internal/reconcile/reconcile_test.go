package reconcile

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/orchestrator/internal/policy"
	"github.com/caspervpn/orchestrator/internal/ports"
)

var now = time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// --- mocks -----------------------------------------------------------------

type mockTelemetry struct {
	recs    contracts.Recommendations
	recsErr error
	health  []contracts.HealthEvent
}

func (m *mockTelemetry) Recommendations(context.Context) (contracts.Recommendations, error) {
	return m.recs, m.recsErr
}
func (m *mockTelemetry) ReportHealth(_ context.Context, ev contracts.HealthEvent) error {
	m.health = append(m.health, ev)
	return nil
}

type mockCP struct {
	nodes   map[string]contracts.Node
	updates []contracts.Node
	listErr error
}

func (m *mockCP) ListNodes(context.Context, ports.NodeFilter) ([]contracts.Node, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]contracts.Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (m *mockCP) GetNode(_ context.Context, id string) (contracts.Node, error) {
	n, ok := m.nodes[id]
	if !ok {
		return contracts.Node{}, errors.New("not found")
	}
	return n, nil
}
func (m *mockCP) UpdateNode(_ context.Context, n contracts.Node) (contracts.Node, error) {
	m.updates = append(m.updates, n)
	m.nodes[n.ID] = n
	return n, nil
}

type provCall struct {
	op     string // up / rotate / down
	arg    string // nodeID or region/cloud
	atTime int    // global sequence number
}

type mockProv struct {
	seq      *int
	calls    []provCall
	upErr    error
	rotErr   error
	downErr  error
	onUp     func()
	onRotate func(nodeID string)
	onDown   func(nodeID string)
}

func (m *mockProv) rec(op, arg string) {
	*m.seq++
	m.calls = append(m.calls, provCall{op: op, arg: arg, atTime: *m.seq})
}
func (m *mockProv) NodeUp(_ context.Context, region, cloud string) error {
	m.rec("up", region+"/"+cloud)
	if m.upErr != nil {
		return m.upErr
	}
	if m.onUp != nil {
		m.onUp()
	}
	return nil
}
func (m *mockProv) NodeRotate(_ context.Context, nodeID string) error {
	m.rec("rotate", nodeID)
	if m.rotErr != nil {
		return m.rotErr
	}
	if m.onRotate != nil {
		m.onRotate(nodeID)
	}
	return nil
}
func (m *mockProv) NodeDown(_ context.Context, nodeID string) error {
	m.rec("down", nodeID)
	if m.downErr != nil {
		return m.downErr
	}
	if m.onDown != nil {
		m.onDown(nodeID)
	}
	return nil
}

type mockProber struct {
	status contracts.HealthStatus
	probed []string
}

func (m *mockProber) Probe(_ context.Context, n contracts.Node) (contracts.HealthEvent, error) {
	m.probed = append(m.probed, n.ID)
	return contracts.HealthEvent{
		NodeID: n.ID, Status: m.status, ProbeSource: "test-probe", ObservedAt: now.Add(-time.Minute),
	}, nil
}

// --- helpers ----------------------------------------------------------------

func activeNode(id string) contracts.Node {
	return contracts.Node{
		ID: id, Role: contracts.NodeRoleEntry, Status: contracts.NodeStatusActive,
		Provider: "hetzner", Cloud: "cloud-a", Region: "eu-central",
		EntryIP: "192.0.2.10", EphemeralEntryIP: true, CreatedAt: now.Add(-24 * time.Hour),
	}
}

func blockedRecs(nodeID string, conf contracts.RecommendationConfidence) contracts.Recommendations {
	return contracts.Recommendations{
		GeneratedAt: now.Add(-time.Minute), WindowSeconds: 300,
		NodeBlocks: []contracts.NodeBlock{{
			Action: contracts.RecommendationMarkNodeBlocked, NodeID: nodeID,
			Regions: []string{"RU-MOW"}, Confidence: conf, Reason: "dpi reset spike",
		}},
	}
}

func thresholds() policy.Thresholds {
	return policy.Thresholds{
		RecommendationMaxAge: 15 * time.Minute,
		ProbeMaxAge:          10 * time.Minute,
		DrainGrace:           30 * time.Minute,
		MaxActionsPerCycle:   5,
	}
}

func newLoop(tel *mockTelemetry, cp *mockCP, prov *mockProv, prober ports.Prober, opts Options) *Loop {
	seq := 0
	prov.seq = &seq
	opts.Thresholds = thresholds()
	return New(Deps{Telemetry: tel, CP: cp, Prov: prov, Prober: prober, Clock: fixedClock{now}}, opts)
}

// --- tests -------------------------------------------------------------------

// Dry-run e2e: a blocked recommendation becomes a plan but the Provisioner and
// the control-plane are NEVER touched.
func TestDryRunPlansWithoutSideEffects(t *testing.T) {
	tel := &mockTelemetry{recs: blockedRecs("node-1", contracts.RecommendationAuthoritative)}
	cp := &mockCP{nodes: map[string]contracts.Node{"node-1": activeNode("node-1")}}
	prov := &mockProv{}

	rep, err := newLoop(tel, cp, prov, nil, Options{DryRun: true}).Cycle(context.Background())
	if err != nil {
		t.Fatalf("Cycle() error: %v", err)
	}
	if !rep.DryRun || len(rep.Plan) != 1 || rep.Plan[0].Type != policy.ActionRotate {
		t.Fatalf("plan = %+v, want single rotate in dry-run", rep.Plan)
	}
	if len(prov.calls) != 0 {
		t.Fatalf("dry-run called the provisioner: %+v", prov.calls)
	}
	if len(cp.updates) != 0 {
		t.Fatalf("dry-run wrote to the control-plane: %+v", cp.updates)
	}
}

// Dry-run must not write anywhere: no infra, no control-plane, and no probe
// verdict reported back to telemetry (reporting steers future recommendations).
func TestDryRunReportsNothingToTelemetry(t *testing.T) {
	tel := &mockTelemetry{recs: blockedRecs("node-1", contracts.RecommendationCorroborated)}
	cp := &mockCP{nodes: map[string]contracts.Node{"node-1": activeNode("node-1")}}
	prov := &mockProv{}
	prober := &mockProber{status: contracts.HealthBlocked}

	if _, err := newLoop(tel, cp, prov, prober, Options{DryRun: true, ProbeEnabled: true}).Cycle(context.Background()); err != nil {
		t.Fatalf("Cycle() error: %v", err)
	}
	if len(prober.probed) != 1 {
		t.Fatalf("probes = %d, want 1 (probing is read-only and still runs)", len(prober.probed))
	}
	if len(tel.health) != 0 {
		t.Fatalf("dry-run reported %d health events to telemetry, want 0", len(tel.health))
	}
	if len(prov.calls) != 0 || len(cp.updates) != 0 {
		t.Fatalf("dry-run mutated state: prov=%+v cp=%+v", prov.calls, cp.updates)
	}
}

// Full rotate e2e: authoritative block → provisioner rotate → control-plane
// carries the new entry IP (the script updates it; the loop verifies).
func TestAuthoritativeBlockRotatesNode(t *testing.T) {
	tel := &mockTelemetry{recs: blockedRecs("node-1", contracts.RecommendationAuthoritative)}
	cp := &mockCP{nodes: map[string]contracts.Node{"node-1": activeNode("node-1")}}
	prov := &mockProv{}
	prov.onRotate = func(nodeID string) { // simulate node_rotate.sh patching the CP
		n := cp.nodes[nodeID]
		n.EntryIP = "198.51.100.7"
		cp.nodes[nodeID] = n
	}

	rep, err := newLoop(tel, cp, prov, nil, Options{}).Cycle(context.Background())
	if err != nil {
		t.Fatalf("Cycle() error: %v", err)
	}
	if rep.Executed != 1 || len(prov.calls) != 1 || prov.calls[0].op != "rotate" || prov.calls[0].arg != "node-1" {
		t.Fatalf("provisioner calls = %+v, executed = %d", prov.calls, rep.Executed)
	}
	if cp.nodes["node-1"].EntryIP != "198.51.100.7" {
		t.Fatalf("control-plane entry_ip = %s, want rotated", cp.nodes["node-1"].EntryIP)
	}
}

// THE anti-poisoning e2e: corroborated field noise without probe confirmation
// must only degrade the node — never touch infra.
func TestFieldNoiseNeverTouchesInfra(t *testing.T) {
	tel := &mockTelemetry{recs: blockedRecs("node-1", contracts.RecommendationCorroborated)}
	cp := &mockCP{nodes: map[string]contracts.Node{"node-1": activeNode("node-1")}}
	prov := &mockProv{}

	if _, err := newLoop(tel, cp, prov, nil, Options{}).Cycle(context.Background()); err != nil {
		t.Fatalf("Cycle() error: %v", err)
	}
	if len(prov.calls) != 0 {
		t.Fatalf("field noise reached the provisioner: %+v", prov.calls)
	}
	if got := cp.nodes["node-1"].Status; got != contracts.NodeStatusDegraded {
		t.Fatalf("status = %s, want degraded (the only allowed effect)", got)
	}
}

// Probe confirmation upgrades a corroborated verdict to a rotation, and the
// verdict is reported back to telemetry.
func TestProbeConfirmationEnablesRotation(t *testing.T) {
	tel := &mockTelemetry{recs: blockedRecs("node-1", contracts.RecommendationCorroborated)}
	cp := &mockCP{nodes: map[string]contracts.Node{"node-1": activeNode("node-1")}}
	prov := &mockProv{}
	prober := &mockProber{status: contracts.HealthBlocked}

	if _, err := newLoop(tel, cp, prov, prober, Options{ProbeEnabled: true}).Cycle(context.Background()); err != nil {
		t.Fatalf("Cycle() error: %v", err)
	}
	if len(prov.calls) != 1 || prov.calls[0].op != "rotate" {
		t.Fatalf("provisioner calls = %+v, want one rotate", prov.calls)
	}
	if len(tel.health) != 1 || tel.health[0].NodeID != "node-1" {
		t.Fatalf("probe verdict not reported to telemetry: %+v", tel.health)
	}
}

// Replacement e2e with strict ordering: node_up BEFORE the old node starts
// draining; the old node is NOT retired in the same cycle (coexistence).
func TestReplaceProvisionsBeforeDraining(t *testing.T) {
	n := activeNode("node-1")
	n.EphemeralEntryIP = false // static entry → replace, not rotate
	tel := &mockTelemetry{recs: blockedRecs("node-1", contracts.RecommendationAuthoritative)}
	cp := &mockCP{nodes: map[string]contracts.Node{"node-1": n}}
	prov := &mockProv{}

	if _, err := newLoop(tel, cp, prov, nil, Options{}).Cycle(context.Background()); err != nil {
		t.Fatalf("Cycle() error: %v", err)
	}
	if len(prov.calls) != 1 || prov.calls[0].op != "up" || prov.calls[0].arg != "eu-central/cloud-a" {
		t.Fatalf("provisioner calls = %+v, want node_up with the node's placement", prov.calls)
	}
	got := cp.nodes["node-1"]
	if got.Status != contracts.NodeStatusDraining {
		t.Fatalf("old node status = %s, want draining", got.Status)
	}
	if _, err := time.Parse(time.RFC3339, got.Labels[policy.DrainStartedLabel]); err != nil {
		t.Fatalf("drain label missing/bad: %q", got.Labels[policy.DrainStartedLabel])
	}
	// Ordering: the only CP write happened after the up call — verified by the
	// fact that a failed up leaves the node untouched (next test).
	for _, c := range prov.calls {
		if c.op == "down" {
			t.Fatal("old node torn down in the same cycle — must coexist through drain grace")
		}
	}
}

// Mid-plan failure: if provisioning the replacement fails, the old node is
// left exactly as it was (no rollback needed, no cascade).
func TestFailedReplacementLeavesOldNodeUntouched(t *testing.T) {
	n := activeNode("node-1")
	n.EphemeralEntryIP = false
	tel := &mockTelemetry{recs: blockedRecs("node-1", contracts.RecommendationAuthoritative)}
	cp := &mockCP{nodes: map[string]contracts.Node{"node-1": n}}
	prov := &mockProv{upErr: errors.New("terraform: quota exceeded")}

	_, err := newLoop(tel, cp, prov, nil, Options{}).Cycle(context.Background())
	if err == nil {
		t.Fatal("want error from failed provisioning")
	}
	if got := cp.nodes["node-1"].Status; got != contracts.NodeStatusActive {
		t.Fatalf("old node status = %s, want active (untouched)", got)
	}
	if len(cp.updates) != 0 {
		t.Fatalf("control-plane written despite failed up: %+v", cp.updates)
	}
}

// Drained long enough → retired via node_down; registry marked retired.
func TestDrainGraceElapsedRetires(t *testing.T) {
	n := activeNode("node-1")
	n.Status = contracts.NodeStatusDraining
	n.Labels = map[string]string{policy.DrainStartedLabel: now.Add(-time.Hour).Format(time.RFC3339)}
	tel := &mockTelemetry{}
	cp := &mockCP{nodes: map[string]contracts.Node{"node-1": n}}
	prov := &mockProv{}

	if _, err := newLoop(tel, cp, prov, nil, Options{}).Cycle(context.Background()); err != nil {
		t.Fatalf("Cycle() error: %v", err)
	}
	if len(prov.calls) != 1 || prov.calls[0].op != "down" {
		t.Fatalf("provisioner calls = %+v, want one down", prov.calls)
	}
	if got := cp.nodes["node-1"].Status; got != contracts.NodeStatusRetired {
		t.Fatalf("status = %s, want retired", got)
	}
}

// Scheduled rotation works even when telemetry is down (recommendations are
// best-effort; fleet state is the only hard dependency).
func TestScheduledRotationSurvivesTelemetryOutage(t *testing.T) {
	due := now.Add(-time.Minute)
	n := activeNode("node-1")
	n.RotateAfter = &due
	tel := &mockTelemetry{recsErr: errors.New("telemetry down")}
	cp := &mockCP{nodes: map[string]contracts.Node{"node-1": n}}
	prov := &mockProv{}

	rep, err := newLoop(tel, cp, prov, nil, Options{}).Cycle(context.Background())
	if err != nil {
		t.Fatalf("Cycle() error: %v", err)
	}
	if rep.Executed != 1 || len(prov.calls) != 1 || prov.calls[0].op != "rotate" {
		t.Fatalf("calls = %+v, want scheduled rotate despite telemetry outage", prov.calls)
	}
}

// No fleet state → no actions at all.
func TestFleetStateUnavailableAborts(t *testing.T) {
	tel := &mockTelemetry{recs: blockedRecs("node-1", contracts.RecommendationAuthoritative)}
	cp := &mockCP{listErr: errors.New("cp down")}
	prov := &mockProv{}

	if _, err := newLoop(tel, cp, prov, nil, Options{}).Cycle(context.Background()); err == nil {
		t.Fatal("want error when fleet state is unavailable")
	}
	if len(prov.calls) != 0 {
		t.Fatalf("acted without fleet state: %+v", prov.calls)
	}
}

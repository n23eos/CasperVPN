package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

var now = time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

func defaults() Thresholds {
	return Thresholds{
		RecommendationMaxAge: 15 * time.Minute,
		ProbeMaxAge:          10 * time.Minute,
		DrainGrace:           30 * time.Minute,
		MaxActionsPerCycle:   10, // roomy default; the cap has its own test
	}
}

func entryNode(id string, status contracts.NodeStatus) contracts.Node {
	return contracts.Node{
		ID: id, Role: contracts.NodeRoleEntry, Status: status,
		Provider: "hetzner", Cloud: "cloud-a", Region: "eu-central",
		EntryIP: "192.0.2.10", EphemeralEntryIP: true,
		CreatedAt: now.Add(-24 * time.Hour),
	}
}

func block(nodeID string, conf contracts.RecommendationConfidence) contracts.NodeBlock {
	return contracts.NodeBlock{
		Action: contracts.RecommendationMarkNodeBlocked, NodeID: nodeID,
		Regions: []string{"RU-MOW"}, Confidence: conf, Reason: "dpi reset spike",
	}
}

func recs(blocks ...contracts.NodeBlock) contracts.Recommendations {
	return contracts.Recommendations{GeneratedAt: now.Add(-time.Minute), WindowSeconds: 300, NodeBlocks: blocks}
}

func one(t *testing.T, actions []Action, nodeID string) Action {
	t.Helper()
	var found []Action
	for _, a := range actions {
		if a.NodeID == nodeID {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly 1 action for %s, got %d: %+v", nodeID, len(found), found)
	}
	return found[0]
}

// THE core safety test: anonymous field noise (corroborated verdict) without an
// own-probe confirmation must NEVER rotate or replace a node.
func TestCorroboratedWithoutProbeNeverRotates(t *testing.T) {
	in := Input{
		Now:   now,
		Recs:  recs(block("node-1", contracts.RecommendationCorroborated)),
		Nodes: []contracts.Node{entryNode("node-1", contracts.NodeStatusActive)},
		// Probes intentionally empty.
	}
	a := one(t, Plan(in, defaults()), "node-1")
	if a.Type != ActionMarkDegraded {
		t.Fatalf("action = %s, want mark_degraded (anti-poisoning gate)", a.Type)
	}
	if !strings.Contains(a.Reason, "anti-poisoning") {
		t.Errorf("reason should explain the gate, got %q", a.Reason)
	}
}

func TestAuthoritativeBlockRotatesEphemeralEntry(t *testing.T) {
	in := Input{
		Now:   now,
		Recs:  recs(block("node-1", contracts.RecommendationAuthoritative)),
		Nodes: []contracts.Node{entryNode("node-1", contracts.NodeStatusActive)},
	}
	a := one(t, Plan(in, defaults()), "node-1")
	if a.Type != ActionRotate {
		t.Fatalf("action = %s, want rotate", a.Type)
	}
}

func TestAuthoritativeBlockReplacesStaticNode(t *testing.T) {
	n := entryNode("node-1", contracts.NodeStatusActive)
	n.EphemeralEntryIP = false
	in := Input{Now: now, Recs: recs(block("node-1", contracts.RecommendationAuthoritative)), Nodes: []contracts.Node{n}}
	a := one(t, Plan(in, defaults()), "node-1")
	if a.Type != ActionReplace {
		t.Fatalf("action = %s, want replace", a.Type)
	}
}

func TestCorroboratedConfirmedByFreshProbeRotates(t *testing.T) {
	in := Input{
		Now:   now,
		Recs:  recs(block("node-1", contracts.RecommendationCorroborated)),
		Nodes: []contracts.Node{entryNode("node-1", contracts.NodeStatusActive)},
		Probes: map[string]contracts.HealthEvent{
			"node-1": {NodeID: "node-1", Status: contracts.HealthBlocked,
				ProbeSource: "ru-probe-1", ObservedAt: now.Add(-2 * time.Minute)},
		},
	}
	a := one(t, Plan(in, defaults()), "node-1")
	if a.Type != ActionRotate {
		t.Fatalf("action = %s, want rotate", a.Type)
	}
}

func TestStaleProbeDoesNotConfirm(t *testing.T) {
	in := Input{
		Now:   now,
		Recs:  recs(block("node-1", contracts.RecommendationCorroborated)),
		Nodes: []contracts.Node{entryNode("node-1", contracts.NodeStatusActive)},
		Probes: map[string]contracts.HealthEvent{
			"node-1": {NodeID: "node-1", Status: contracts.HealthBlocked,
				ProbeSource: "ru-probe-1", ObservedAt: now.Add(-time.Hour)},
		},
	}
	if a := one(t, Plan(in, defaults()), "node-1"); a.Type != ActionMarkDegraded {
		t.Fatalf("action = %s, want mark_degraded (probe too old)", a.Type)
	}
}

func TestHealthyProbeVetoesCorroboratedBlock(t *testing.T) {
	in := Input{
		Now:   now,
		Recs:  recs(block("node-1", contracts.RecommendationCorroborated)),
		Nodes: []contracts.Node{entryNode("node-1", contracts.NodeStatusActive)},
		Probes: map[string]contracts.HealthEvent{
			"node-1": {NodeID: "node-1", Status: contracts.HealthHealthy,
				ProbeSource: "ru-probe-1", ObservedAt: now.Add(-time.Minute)},
		},
	}
	if a := one(t, Plan(in, defaults()), "node-1"); a.Type != ActionMarkDegraded {
		t.Fatalf("action = %s, want mark_degraded (our probe disagrees)", a.Type)
	}
}

func TestStaleRecommendationsNoAction(t *testing.T) {
	r := recs(block("node-1", contracts.RecommendationAuthoritative))
	r.GeneratedAt = now.Add(-2 * time.Hour)
	in := Input{Now: now, Recs: r, Nodes: []contracts.Node{entryNode("node-1", contracts.NodeStatusActive)}}
	if a := one(t, Plan(in, defaults()), "node-1"); a.Type != ActionNoop {
		t.Fatalf("action = %s, want noop for stale window", a.Type)
	}
}

func TestUnknownNodeIgnored(t *testing.T) {
	in := Input{Now: now, Recs: recs(block("ghost", contracts.RecommendationAuthoritative))}
	if a := one(t, Plan(in, defaults()), "ghost"); a.Type != ActionNoop {
		t.Fatalf("action = %s, want noop for unknown node", a.Type)
	}
}

func TestMalformedBlockIgnored(t *testing.T) {
	b := block("node-1", contracts.RecommendationAuthoritative)
	b.Regions = nil // fails contract validation
	in := Input{Now: now, Recs: recs(b), Nodes: []contracts.Node{entryNode("node-1", contracts.NodeStatusActive)}}
	if got := Plan(in, defaults()); len(got) != 0 {
		t.Fatalf("malformed recommendation produced actions: %+v", got)
	}
}

func TestDrainingAndRetiredNodesUntouched(t *testing.T) {
	for _, st := range []contracts.NodeStatus{
		contracts.NodeStatusRetired, contracts.NodeStatusProvisioning,
	} {
		in := Input{
			Now:   now,
			Recs:  recs(block("node-1", contracts.RecommendationAuthoritative)),
			Nodes: []contracts.Node{entryNode("node-1", st)},
		}
		if a := one(t, Plan(in, defaults()), "node-1"); a.Type != ActionNoop {
			t.Errorf("status %s: action = %s, want noop", st, a.Type)
		}
	}
}

func TestHealthyFleetEmptyPlan(t *testing.T) {
	in := Input{
		Now:   now,
		Recs:  contracts.Recommendations{GeneratedAt: now.Add(-time.Minute), WindowSeconds: 300},
		Nodes: []contracts.Node{entryNode("node-1", contracts.NodeStatusActive), entryNode("node-2", contracts.NodeStatusActive)},
	}
	if got := Plan(in, defaults()); len(got) != 0 {
		t.Fatalf("healthy fleet produced actions: %+v", got)
	}
}

func TestScheduledRotationByRotateAfter(t *testing.T) {
	due := now.Add(-time.Minute)
	n := entryNode("node-1", contracts.NodeStatusActive)
	n.RotateAfter = &due
	notYet := now.Add(time.Hour)
	m := entryNode("node-2", contracts.NodeStatusActive)
	m.RotateAfter = &notYet

	got := Plan(Input{Now: now, Nodes: []contracts.Node{n, m}}, defaults())
	if a := one(t, got, "node-1"); a.Type != ActionRotate {
		t.Fatalf("node-1 action = %s, want rotate (rotate_after elapsed)", a.Type)
	}
	for _, a := range got {
		if a.NodeID == "node-2" {
			t.Fatalf("node-2 rotated ahead of schedule: %+v", a)
		}
	}
}

func TestRetireAfterDrainGrace(t *testing.T) {
	old := entryNode("old", contracts.NodeStatusDraining)
	old.Labels = map[string]string{DrainStartedLabel: now.Add(-time.Hour).Format(time.RFC3339)}
	fresh := entryNode("fresh", contracts.NodeStatusDraining)
	fresh.Labels = map[string]string{DrainStartedLabel: now.Add(-time.Minute).Format(time.RFC3339)}
	unlabeled := entryNode("unlabeled", contracts.NodeStatusDraining)

	got := Plan(Input{Now: now, Nodes: []contracts.Node{old, fresh, unlabeled}}, defaults())
	if a := one(t, got, "old"); a.Type != ActionRetire {
		t.Fatalf("old: action = %s, want retire", a.Type)
	}
	for _, a := range got {
		if a.NodeID == "fresh" {
			t.Fatalf("fresh draining node retired before grace: %+v", a)
		}
	}
	if a := one(t, got, "unlabeled"); a.Type != ActionNoop {
		t.Fatalf("unlabeled: action = %s, want noop (no trustworthy drain timestamp)", a.Type)
	}
}

// Even fully poisoned/authoritative-looking input can burn at most
// MaxActionsPerCycle nodes per cycle — the fleet is never stampeded.
func TestSideEffectCapPreventsFleetStampede(t *testing.T) {
	th := defaults()
	th.MaxActionsPerCycle = 1
	in := Input{
		Now: now,
		Recs: recs(
			block("node-1", contracts.RecommendationAuthoritative),
			block("node-2", contracts.RecommendationAuthoritative),
			block("node-3", contracts.RecommendationAuthoritative),
		),
		Nodes: []contracts.Node{
			entryNode("node-1", contracts.NodeStatusActive),
			entryNode("node-2", contracts.NodeStatusActive),
			entryNode("node-3", contracts.NodeStatusActive),
		},
	}
	side := 0
	for _, a := range Plan(in, th) {
		if a.Type == ActionRotate || a.Type == ActionReplace || a.Type == ActionRetire {
			side++
		}
	}
	if side != 1 {
		t.Fatalf("side-effecting actions = %d, want exactly 1 (cap)", side)
	}
}

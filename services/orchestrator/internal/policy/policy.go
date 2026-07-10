// Package policy is the orchestrator's brain: it turns telemetry
// recommendations, the control-plane node registry and own-probe verdicts into
// a plan of actions. It is 100% pure — no I/O, no side effects — so every
// safety property is unit-testable.
//
// The central safety property is the ANTI-POISONING GATE: anonymous field
// signals alone must never rotate the fleet. An irreversible action (rotate /
// replace / retire) is planned only when the verdict is authoritative (derived
// from an own-probe HealthEvent) or when a corroborated field verdict is
// confirmed by a fresh own probe. Anything weaker degrades the node at most.
package policy

import (
	"fmt"
	"sort"
	"time"

	"github.com/caspervpn/contracts"
)

// ActionType is what the reconcile loop should do to one node.
type ActionType string

// ActionType values, ordered from harmless to irreversible.
const (
	ActionNoop         ActionType = "noop"
	ActionMarkDegraded ActionType = "mark_degraded"
	ActionRotate       ActionType = "rotate"  // fresh ephemeral entry IP + REALITY re-key
	ActionReplace      ActionType = "replace" // provision replacement, drain old
	ActionRetire       ActionType = "retire"  // tear down a drained node
)

// sideEffecting reports whether the action touches infra.
func (a ActionType) sideEffecting() bool {
	switch a {
	case ActionRotate, ActionReplace, ActionRetire:
		return true
	}
	return false
}

// Action is one planned step. Reason always explains WHY, including why a
// stronger action was withheld — the dry-run log is an operator artifact.
type Action struct {
	Type    ActionType
	NodeID  string
	Regions []string // regions the trigger applies to (for logs/scoping)
	Reason  string
}

// Thresholds are the policy knobs (env-tunable, see config).
type Thresholds struct {
	// RecommendationMaxAge: a Recommendations payload older than this window is
	// stale and must not trigger side effects.
	RecommendationMaxAge time.Duration
	// ProbeMaxAge: an own-probe verdict older than this cannot confirm a
	// corroborated recommendation.
	ProbeMaxAge time.Duration
	// DrainGrace: minimum coexistence time before a draining node is retired.
	DrainGrace time.Duration
	// MaxActionsPerCycle caps side-effecting actions per plan. Even a fully
	// poisoned input can burn at most this many nodes per cycle.
	MaxActionsPerCycle int
}

// DrainStartedLabel is the Node label recording when draining began, so the
// grace period survives orchestrator restarts. RFC 3339.
const DrainStartedLabel = "orchestrator.drain_started_at"

// Input is everything the policy engine sees for one evaluation.
type Input struct {
	Now time.Time
	// Recs is the latest telemetry feedback payload.
	Recs contracts.Recommendations
	// Nodes is the current control-plane registry view.
	Nodes []contracts.Node
	// Probes holds the freshest own-probe verdict per node ID. These are
	// authoritative HealthEvents from OUR prober, not anonymous field data.
	Probes map[string]contracts.HealthEvent
}

// Plan computes the full action plan for one reconcile cycle.
func Plan(in Input, th Thresholds) []Action {
	byID := make(map[string]contracts.Node, len(in.Nodes))
	for _, n := range in.Nodes {
		byID[n.ID] = n
	}
	planned := make(map[string]bool, len(in.Nodes)) // nodes already decided
	var out []Action

	add := func(a Action) {
		planned[a.NodeID] = true
		out = append(out, a)
	}

	// 1. Telemetry-driven decisions (block recommendations), gated.
	recsFresh := !in.Recs.GeneratedAt.IsZero() &&
		in.Now.Sub(in.Recs.GeneratedAt) <= th.RecommendationMaxAge
	for _, b := range in.Recs.NodeBlocks {
		if b.Validate() != nil {
			continue // malformed input from the wire never drives actions
		}
		node, known := byID[b.NodeID]
		if !known {
			add(Action{Type: ActionNoop, NodeID: b.NodeID, Regions: b.Regions,
				Reason: "recommendation for unknown node — ignored"})
			continue
		}
		if !recsFresh {
			add(Action{Type: ActionNoop, NodeID: b.NodeID, Regions: b.Regions,
				Reason: fmt.Sprintf("recommendations stale (generated_at=%s, max age %s) — no action",
					in.Recs.GeneratedAt.Format(time.RFC3339), th.RecommendationMaxAge)})
			continue
		}
		switch node.Status {
		case contracts.NodeStatusDraining, contracts.NodeStatusRetired, contracts.NodeStatusProvisioning:
			add(Action{Type: ActionNoop, NodeID: b.NodeID, Regions: b.Regions,
				Reason: fmt.Sprintf("node already %s — no action", node.Status)})
			continue
		}
		if ok, why := gatePassed(b, in.Probes, in.Now, th.ProbeMaxAge); !ok {
			// ANTI-POISONING: unconfirmed field data degrades at most.
			if node.Status == contracts.NodeStatusDegraded {
				add(Action{Type: ActionNoop, NodeID: b.NodeID, Regions: b.Regions,
					Reason: "already degraded; " + why})
			} else {
				add(Action{Type: ActionMarkDegraded, NodeID: b.NodeID, Regions: b.Regions,
					Reason: why})
			}
			continue
		}
		add(Action{Type: replaceOrRotate(node), NodeID: b.NodeID, Regions: b.Regions,
			Reason: fmt.Sprintf("blocked in %v (confidence=%s): %s", b.Regions, b.Confidence, b.Reason)})
	}

	// 2. Scheduled fast rotation: rotate_after elapsed.
	for _, n := range in.Nodes {
		if planned[n.ID] || n.RotateAfter == nil || in.Now.Before(*n.RotateAfter) {
			continue
		}
		if n.Status != contracts.NodeStatusActive && n.Status != contracts.NodeStatusDegraded {
			continue
		}
		add(Action{Type: ActionRotate, NodeID: n.ID,
			Reason: fmt.Sprintf("scheduled rotation: rotate_after=%s elapsed", n.RotateAfter.Format(time.RFC3339))})
	}

	// 3. Retire drained nodes after the grace period (replacement coexists
	//    with the old node until here).
	for _, n := range in.Nodes {
		if planned[n.ID] || n.Status != contracts.NodeStatusDraining {
			continue
		}
		started, err := time.Parse(time.RFC3339, n.Labels[DrainStartedLabel])
		if err != nil {
			// No trustworthy drain timestamp — never guess with a teardown.
			add(Action{Type: ActionNoop, NodeID: n.ID,
				Reason: "draining without valid " + DrainStartedLabel + " label — operator attention required"})
			continue
		}
		if in.Now.Sub(started) >= th.DrainGrace {
			add(Action{Type: ActionRetire, NodeID: n.ID,
				Reason: fmt.Sprintf("drain grace %s elapsed (draining since %s)", th.DrainGrace, started.Format(time.RFC3339))})
		}
	}

	return capSideEffects(out, th.MaxActionsPerCycle)
}

// gatePassed implements the anti-poisoning gate for one block recommendation.
// It returns false with a human-readable reason when only weaker action is
// allowed.
func gatePassed(b contracts.NodeBlock, probes map[string]contracts.HealthEvent, now time.Time, probeMaxAge time.Duration) (bool, string) {
	switch b.Confidence {
	case contracts.RecommendationAuthoritative:
		// Telemetry derived this from an authoritative HealthEvent already.
		return true, ""
	case contracts.RecommendationCorroborated:
		ev, ok := probes[b.NodeID]
		if !ok {
			return false, "anti-poisoning: corroborated field verdict without own-probe confirmation — degrade only"
		}
		if now.Sub(ev.ObservedAt) > probeMaxAge {
			return false, fmt.Sprintf("anti-poisoning: own probe too old (%s > %s) — degrade only",
				now.Sub(ev.ObservedAt).Round(time.Second), probeMaxAge)
		}
		if ev.Status != contracts.HealthBlocked && ev.Status != contracts.HealthUnreachable {
			return false, fmt.Sprintf("anti-poisoning: own probe says %q, not blocked — degrade only", ev.Status)
		}
		return true, ""
	default:
		return false, fmt.Sprintf("anti-poisoning: unknown confidence %q — degrade only", b.Confidence)
	}
}

// replaceOrRotate picks the cheapest sufficient remedy: nodes with an
// ephemeral entry IP get a fresh IP (rotate); everything else needs a full
// replacement.
func replaceOrRotate(n contracts.Node) ActionType {
	if n.EphemeralEntryIP {
		return ActionRotate
	}
	return ActionReplace
}

// capSideEffects keeps at most max side-effecting actions, preferring retire
// (finishing an in-flight replacement) over new rotations/replacements, and
// keeps every non-side-effecting action. Deterministic order for tests/logs.
func capSideEffects(actions []Action, max int) []Action {
	if max <= 0 {
		max = 1
	}
	rank := func(t ActionType) int {
		switch t {
		case ActionRetire:
			return 0
		case ActionRotate:
			return 1
		case ActionReplace:
			return 2
		}
		return 3
	}
	sort.SliceStable(actions, func(i, j int) bool {
		if actions[i].Type.sideEffecting() != actions[j].Type.sideEffecting() {
			return !actions[i].Type.sideEffecting() // harmless first, always kept
		}
		return rank(actions[i].Type) < rank(actions[j].Type)
	})
	kept := make([]Action, 0, len(actions))
	side := 0
	for _, a := range actions {
		if a.Type.sideEffecting() {
			if side >= max {
				kept = append(kept, Action{Type: ActionNoop, NodeID: a.NodeID, Regions: a.Regions,
					Reason: fmt.Sprintf("deferred (max %d side-effecting actions per cycle): wanted %s — %s", max, a.Type, a.Reason)})
				continue
			}
			side++
		}
		kept = append(kept, a)
	}
	return kept
}

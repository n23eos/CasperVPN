// Package personalize narrows the raw node set for one request: it drops blocked
// nodes, filters by plan / region / platform, and enforces the [ANTI-BLOCK]
// invariant that a subscription never collapses to a transport monoculture.
// Per-user secrets are stamped later by the frozen contract renderers.
package personalize

import (
	"github.com/caspervpn/contracts"
)

const (
	// minTransportDiversity is the floor on distinct transport families in a
	// served subscription. Below this the client cannot switch families when one
	// is blocked, defeating the whole design — so filtering must never push under
	// it while diversity is available.
	minTransportDiversity = 2
	// maxBasicNodes caps how many nodes the basic tier receives.
	maxBasicNodes = 5
	// labelBlocked marks a node blocked out-of-band (in addition to NodeStatus).
	labelBlocked = "blocked"
	// labelTier gates nodes by plan tier ("premium" nodes are unlimited-only).
	labelTier = "tier"
	// labelPlatformExclude lists platforms a node opts out of (comma-free single
	// value match; operational escape hatch).
	labelPlatformExclude = "platform_exclude"
)

// Params carries the per-request personalization hints (client-supplied).
type Params struct {
	Region   string
	Platform contracts.Platform
}

// Result reports the outcome of filtering.
type Result struct {
	Bundle  contracts.SubscriptionBundle
	Types   []contracts.TransportType // distinct enabled families served
	Relaxed bool                      // filters were relaxed to preserve diversity
}

// Filter applies personalization to a resolved bundle for the given plan/params.
func Filter(b contracts.SubscriptionBundle, plan contracts.SubscriptionPlan, p Params) Result {
	servable := dropBlocked(b.Nodes)

	kept := servable
	kept = filterRegion(kept, p.Region)
	kept = filterPlatform(kept, p.Platform)
	kept = filterPlan(kept, plan)

	relaxed := false
	// Never let personalization create a monoculture while diversity exists in
	// the servable set: fall back to the full servable set if it does.
	if len(DistinctTransportTypes(kept)) < minTransportDiversity &&
		len(DistinctTransportTypes(servable)) >= minTransportDiversity {
		kept = servable
		relaxed = true
	}

	out := b
	out.Nodes = kept
	return Result{Bundle: out, Types: DistinctTransportTypes(kept), Relaxed: relaxed}
}

// dropBlocked keeps only nodes that can serve traffic (active/degraded) and are
// not flagged blocked.
func dropBlocked(nodes []contracts.Node) []contracts.Node {
	out := make([]contracts.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Status != contracts.NodeStatusActive && n.Status != contracts.NodeStatusDegraded {
			continue
		}
		if n.Labels[labelBlocked] == "true" {
			continue
		}
		if len(enabled(n)) == 0 {
			continue
		}
		out = append(out, n)
	}
	return out
}

// filterRegion prefers region matches but never strands the user: if no node
// matches, the region hint is ignored.
func filterRegion(nodes []contracts.Node, region string) []contracts.Node {
	if region == "" {
		return nodes
	}
	var match []contracts.Node
	for _, n := range nodes {
		if n.Region == region {
			match = append(match, n)
		}
	}
	if len(match) == 0 {
		return nodes
	}
	return match
}

// filterPlatform drops nodes that opted out of the requesting platform.
func filterPlatform(nodes []contracts.Node, platform contracts.Platform) []contracts.Node {
	if platform == "" {
		return nodes
	}
	out := make([]contracts.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Labels[labelPlatformExclude] == string(platform) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// filterPlan applies tier gating and the basic-tier node cap.
func filterPlan(nodes []contracts.Node, plan contracts.SubscriptionPlan) []contracts.Node {
	if plan == contracts.SubscriptionPlanUnlimited {
		return nodes
	}
	// basic: exclude premium-tier nodes, then cap the count.
	out := make([]contracts.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Labels[labelTier] == "premium" {
			continue
		}
		out = append(out, n)
		if len(out) >= maxBasicNodes {
			break
		}
	}
	return out
}

// DistinctTransportTypes returns the distinct enabled transport families across
// nodes, in first-seen order.
func DistinctTransportTypes(nodes []contracts.Node) []contracts.TransportType {
	seen := map[contracts.TransportType]bool{}
	var out []contracts.TransportType
	for _, n := range nodes {
		for _, t := range enabled(n) {
			if !seen[t.Type] {
				seen[t.Type] = true
				out = append(out, t.Type)
			}
		}
	}
	return out
}

func enabled(n contracts.Node) []contracts.Transport {
	out := make([]contracts.Transport, 0, len(n.Transports))
	for _, t := range n.Transports {
		if t.Enabled {
			out = append(out, t)
		}
	}
	return out
}

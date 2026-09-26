package aggregate

import (
	"fmt"
	"sort"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/telemetry/internal/config"
)

// Action is a control-plane instruction produced by the feedback loop.
type Action string

const (
	ActionMarkNodeBlocked     Action = "mark_node_blocked"
	ActionPrioritizeTransport Action = "prioritize_transport"
)

// Confidence expresses how the verdict was reached — the orchestrator can gate on it.
type Confidence string

const (
	ConfidenceAuthoritative Confidence = "authoritative" // orchestrator probe (HealthEvent)
	ConfidenceCorroborated  Confidence = "corroborated"  // independent field sources agree
)

// NodeBlock describes an authenticated probe verdict for control-plane.
type NodeBlock struct {
	Action     Action     `json:"action"`
	NodeID     string     `json:"node_id"`
	Regions    []string   `json:"regions"`
	Confidence Confidence `json:"confidence"`
	Reason     string     `json:"reason"`
}

// TransportRank scores one transport's health in a region (1 = best, 0 = dead).
type TransportRank struct {
	Transport contracts.TransportType `json:"transport"`
	Score     float64                 `json:"score"`
	Dead      bool                    `json:"dead"`
	Sources   int                     `json:"sources"`
}

// RegionPriority tells control-plane which transport to prefer in a region.
type RegionPriority struct {
	Action      Action                  `json:"action"`
	Region      string                  `json:"region"`
	Recommended contracts.TransportType `json:"recommended_transport"`
	Ranked      []TransportRank         `json:"ranked"`
	Reason      string                  `json:"reason"`
}

// Recommendations is the full control-plane payload for one evaluation.
type Recommendations struct {
	GeneratedAt      time.Time        `json:"generated_at"`
	WindowSeconds    int              `json:"window_seconds"`
	NodeBlocks       []NodeBlock      `json:"node_blocks"`
	RegionPriorities []RegionPriority `json:"region_priorities"`
}

// Recommend emits actions only from authenticated infrastructure probes.
// Client-reported ASN/ISP fields are observations, not proof of independent
// sources. Their statistical aggregates remain available through /v1/aggregates.
func Recommend(_ []contracts.FieldSignal, health []contracts.HealthEvent, now time.Time, window time.Duration, _ config.VerdictParams) Recommendations {
	return Recommendations{
		GeneratedAt:      now,
		WindowSeconds:    int(window / time.Second),
		NodeBlocks:       nodeBlocks(health, now, window),
		RegionPriorities: []RegionPriority{},
	}
}

func nodeBlocks(health []contracts.HealthEvent, now time.Time, window time.Duration) []NodeBlock {
	from := now.Add(-window)

	// regions[node][region] = worst confidence seen for that node+region.
	regions := map[string]map[string]Confidence{}
	reasons := map[string]string{}
	add := func(node, region string, c Confidence, reason string) {
		if region == "" {
			return
		}
		if regions[node] == nil {
			regions[node] = map[string]Confidence{}
		}
		// Authoritative outranks corroborated.
		if cur, ok := regions[node][region]; !ok || c == ConfidenceAuthoritative && cur != ConfidenceAuthoritative {
			regions[node][region] = c
		}
		if _, ok := reasons[node]; !ok {
			reasons[node] = reason
		}
	}

	// (a) Authoritative: orchestrator probes.
	for _, e := range health {
		if e.ObservedAt.Before(from) || e.ObservedAt.After(now) {
			continue
		}
		if e.Status != contracts.HealthBlocked && e.Status != contracts.HealthUnreachable {
			continue
		}
		regs := e.BlockedFromRegions
		for _, r := range regs {
			add(e.NodeID, r, ConfidenceAuthoritative,
				fmt.Sprintf("probe %s reports %s", e.ProbeSource, e.Status))
		}
	}

	// Flatten to sorted, deterministic output.
	nodes := make([]string, 0, len(regions))
	for n := range regions {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	var out []NodeBlock
	for _, n := range nodes {
		regs := make([]string, 0, len(regions[n]))
		conf := ConfidenceCorroborated
		for r, c := range regions[n] {
			regs = append(regs, r)
			if c == ConfidenceAuthoritative {
				conf = ConfidenceAuthoritative
			}
		}
		sort.Strings(regs)
		out = append(out, NodeBlock{
			Action:     ActionMarkNodeBlocked,
			NodeID:     n,
			Regions:    regs,
			Confidence: conf,
			Reason:     reasons[n],
		})
	}
	return out
}

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

// NodeBlock tells control-plane to mark a node blocked in specific regions so the
// orchestrator can rotate/replace it. Every blocked verdict from the field that
// clears the poisoning bar becomes exactly one of these (acceptance criterion 3).
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

// Recommend builds the control-plane payload from anonymous field signals plus
// authoritative health events. Field verdicts must clear the poisoning gates;
// health events are trusted directly (they come from authenticated probes).
func Recommend(signals []contracts.FieldSignal, health []contracts.HealthEvent, now time.Time, window time.Duration, p config.VerdictParams) Recommendations {
	aggs := Compute(signals, now, window, p)
	return Recommendations{
		GeneratedAt:      now,
		WindowSeconds:    int(window / time.Second),
		NodeBlocks:       nodeBlocks(signals, health, now, window, p),
		RegionPriorities: regionPriorities(aggs),
	}
}

// regionPriorities ranks transports per region by health score and names a winner.
func regionPriorities(aggs map[Key]Aggregate) []RegionPriority {
	byRegion := map[string][]TransportRank{}
	for k, a := range aggs {
		byRegion[k.Region] = append(byRegion[k.Region], TransportRank{
			Transport: k.Transport,
			Score:     healthScore(a),
			Dead:      a.Dead,
			Sources:   a.DistinctSources,
		})
	}

	regions := make([]string, 0, len(byRegion))
	for r := range byRegion {
		regions = append(regions, r)
	}
	sort.Strings(regions)

	var out []RegionPriority
	for _, r := range regions {
		ranks := byRegion[r]
		sort.Slice(ranks, func(i, j int) bool {
			if ranks[i].Score != ranks[j].Score {
				return ranks[i].Score > ranks[j].Score // best first
			}
			return ranks[i].Transport < ranks[j].Transport // stable tie-break
		})
		rp := RegionPriority{
			Action: ActionPrioritizeTransport,
			Region: r,
			Ranked: ranks,
		}
		if len(ranks) > 0 && !ranks[0].Dead {
			rp.Recommended = ranks[0].Transport
			rp.Reason = fmt.Sprintf("prefer %s (score %.2f, %d sources)",
				ranks[0].Transport, ranks[0].Score, ranks[0].Sources)
		} else {
			rp.Reason = "all observed transports impaired; hold last-known-good and rotate nodes"
		}
		out = append(out, rp)
	}
	return out
}

// healthScore maps an aggregate to 0..1 (1 = healthy). Dead → 0.
func healthScore(a Aggregate) float64 {
	if a.Dead {
		return 0
	}
	// Reward live successes, penalise blocked/degraded voices. Clamp to [0,1].
	s := 1 - a.BlockedShare - 0.5*a.DegradedShare
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

// nodeKey buckets node-scoped field aggregation.
type nodeKey struct {
	node   string
	region string
}

// nodeBlocks derives per-node blocked regions from (a) authoritative health events
// and (b) field signals that clear the poisoning gates for a (node, region).
func nodeBlocks(signals []contracts.FieldSignal, health []contracts.HealthEvent, now time.Time, window time.Duration, p config.VerdictParams) []NodeBlock {
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

	// (b) Corroborated: field signals per (node, region), gated against poisoning.
	sources := map[nodeKey]map[SourceKey]*srcState{}
	for i := range signals {
		s := signals[i]
		if s.ObservedAt.Before(from) || s.ObservedAt.After(now) {
			continue
		}
		nk := nodeKey{node: s.NodeID, region: s.Region}
		if sources[nk] == nil {
			sources[nk] = map[SourceKey]*srcState{}
		}
		observeInto(sources[nk], sourceKey(s), classify(s))
	}
	for nk, m := range sources {
		distinct, blocked, _, ok := tally(m)
		if distinct < p.MinSources || blocked < p.MinBlockedSources {
			continue
		}
		blockedShare := float64(blocked) / float64(distinct)
		okShare := float64(ok) / float64(distinct)
		if blockedShare >= p.DeadShareThresh && okShare <= p.MaxOKShareForDead {
			add(nk.node, nk.region, ConfidenceCorroborated,
				fmt.Sprintf("%d/%d field sources blocked (share %.2f)", blocked, distinct, blockedShare))
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

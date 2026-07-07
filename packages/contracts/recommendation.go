package contracts

import (
	"fmt"
	"time"
)

// This file is the canonical contract for the telemetry -> orchestrator /
// control-plane feedback payload (Wave 2, TZ-contract-changes §3). The shapes
// mirror the wire format already emitted by the telemetry service
// (GET /v1/recommendations); field names and enum values are stable.
//
// Additive extension of the frozen contract: existing FieldSignal/HealthEvent
// types are untouched.

// RecommendationAction is a control-plane instruction produced by the
// telemetry feedback loop.
type RecommendationAction string

// RecommendationAction values (wire-stable).
const (
	// RecommendationMarkNodeBlocked: mark a node blocked in specific regions so
	// the orchestrator can rotate/replace it.
	RecommendationMarkNodeBlocked RecommendationAction = "mark_node_blocked"
	// RecommendationPrioritizeTransport: prefer a transport in a region.
	RecommendationPrioritizeTransport RecommendationAction = "prioritize_transport"
)

// Valid reports whether a is a known RecommendationAction.
func (a RecommendationAction) Valid() bool {
	switch a {
	case RecommendationMarkNodeBlocked, RecommendationPrioritizeTransport:
		return true
	}
	return false
}

// RecommendationConfidence expresses how a verdict was reached — consumers
// (orchestrator) can gate irreversible actions on it.
type RecommendationConfidence string

// RecommendationConfidence values (wire-stable).
const (
	// RecommendationAuthoritative: from an orchestrator probe (HealthEvent).
	RecommendationAuthoritative RecommendationConfidence = "authoritative"
	// RecommendationCorroborated: independent anonymous field sources agree.
	RecommendationCorroborated RecommendationConfidence = "corroborated"
)

// Valid reports whether c is a known RecommendationConfidence.
func (c RecommendationConfidence) Valid() bool {
	switch c {
	case RecommendationAuthoritative, RecommendationCorroborated:
		return true
	}
	return false
}

// NodeBlock tells control-plane to mark a node blocked in specific regions so
// the orchestrator can rotate/replace it.
type NodeBlock struct {
	Action     RecommendationAction     `json:"action"`
	NodeID     string                   `json:"node_id"`
	Regions    []string                 `json:"regions"`
	Confidence RecommendationConfidence `json:"confidence"`
	Reason     string                   `json:"reason"`
}

// Validate performs shallow structural checks on a NodeBlock.
func (b NodeBlock) Validate() error {
	if b.Action != RecommendationMarkNodeBlocked {
		return fmt.Errorf("contracts: node block requires action %q, got %q",
			RecommendationMarkNodeBlocked, b.Action)
	}
	if b.NodeID == "" {
		return fmt.Errorf("contracts: node block requires node_id")
	}
	if len(b.Regions) == 0 {
		return fmt.Errorf("contracts: node block %q requires regions", b.NodeID)
	}
	if !b.Confidence.Valid() {
		return fmt.Errorf("contracts: unknown confidence %q", b.Confidence)
	}
	return nil
}

// TransportRank scores one transport's health in a region (1 = best, 0 = dead).
type TransportRank struct {
	Transport TransportType `json:"transport"`
	Score     float64       `json:"score"`
	Dead      bool          `json:"dead"`
	Sources   int           `json:"sources"`
}

// RegionPriority tells control-plane which transport to prefer in a region.
// Recommended is empty when every observed transport is impaired (hold
// last-known-good and rotate nodes).
type RegionPriority struct {
	Action      RecommendationAction `json:"action"`
	Region      string               `json:"region"`
	Recommended TransportType        `json:"recommended_transport"`
	Ranked      []TransportRank      `json:"ranked"`
	Reason      string               `json:"reason"`
}

// Validate performs shallow structural checks on a RegionPriority.
func (p RegionPriority) Validate() error {
	if p.Action != RecommendationPrioritizeTransport {
		return fmt.Errorf("contracts: region priority requires action %q, got %q",
			RecommendationPrioritizeTransport, p.Action)
	}
	if p.Region == "" {
		return fmt.Errorf("contracts: region priority requires region")
	}
	if p.Recommended != "" && !p.Recommended.Valid() {
		return fmt.Errorf("contracts: unknown recommended transport %q", p.Recommended)
	}
	return nil
}

// Recommendations is the full feedback payload for one evaluation window —
// the orchestrator's input for node rotation and transport prioritization.
type Recommendations struct {
	GeneratedAt      time.Time        `json:"generated_at"`
	WindowSeconds    int              `json:"window_seconds"`
	NodeBlocks       []NodeBlock      `json:"node_blocks"`
	RegionPriorities []RegionPriority `json:"region_priorities"`
}

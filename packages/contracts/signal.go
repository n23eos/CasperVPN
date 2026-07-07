package contracts

import (
	"fmt"
	"time"
)

// FieldSignal is one anonymous observation reported by a client — the raw fuel
// of the "where did it get blocked" feedback loop. Every field the switching
// logic and the block-map need is here; nothing that identifies a user is.
//
// PRIVACY INVARIANT: a FieldSignal MUST carry no user identity. Geography is
// coarse (region/ASN/ISP), never precise. Correlate by node/transport, not user.
type FieldSignal struct {
	// SignalID is a client-generated dedup key (random, non-identifying).
	SignalID string `json:"signal_id"`

	// What was tried.
	NodeID           string           `json:"node_id"`
	TransportType    TransportType    `json:"transport_type"`
	TransportVersion TransportVersion `json:"transport_version"`

	// Where — coarse network location, so the loop can localize a block.
	Region string `json:"region"`        // coarse geo, e.g. "RU-MOW"
	ASN    int    `json:"asn,omitempty"` // observed autonomous system number
	ISP    string `json:"isp,omitempty"` // observed ISP/operator name

	// Outcome.
	SignalType SignalType `json:"signal_type"`
	Success    bool       `json:"success"`
	RTTms      int        `json:"rtt_ms,omitempty"`   // round-trip time when measurable
	LossPct    float64    `json:"loss_pct,omitempty"` // packet loss 0..100

	// Reporter context (coarse), for slicing the block-map.
	ClientVersion string   `json:"client_version,omitempty"`
	Platform      Platform `json:"platform,omitempty"`

	ObservedAt time.Time `json:"observed_at"`
}

// Validate performs shallow structural checks on a FieldSignal.
func (s FieldSignal) Validate() error {
	if s.SignalID == "" {
		return fmt.Errorf("contracts: field signal requires signal_id")
	}
	if s.NodeID == "" {
		return fmt.Errorf("contracts: field signal requires node_id")
	}
	if !s.TransportType.Valid() {
		return fmt.Errorf("contracts: unknown transport type %q", s.TransportType)
	}
	if !s.SignalType.Valid() {
		return fmt.Errorf("contracts: unknown signal type %q", s.SignalType)
	}
	return nil
}

// HealthEvent is the orchestrator's own verdict on a node from active probing
// (including probes originating inside censored networks). Distinct from
// FieldSignal: this is authoritative/operational, not anonymous client data.
type HealthEvent struct {
	NodeID string       `json:"node_id"`
	Status HealthStatus `json:"status"`

	// ProbeSource is where the probe ran from (e.g. "ru-probe-1"), so a block can
	// be scoped per region.
	ProbeSource string `json:"probe_source"`

	// BlockedFromRegions lists coarse regions where the node currently looks
	// blocked. Empty when healthy everywhere probed.
	BlockedFromRegions []string `json:"blocked_from_regions,omitempty"`

	LatencyMs  int       `json:"latency_ms,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// Validate performs shallow structural checks on a HealthEvent.
func (e HealthEvent) Validate() error {
	if e.NodeID == "" {
		return fmt.Errorf("contracts: health event requires node_id")
	}
	if !e.Status.Valid() {
		return fmt.Errorf("contracts: unknown health status %q", e.Status)
	}
	if e.ProbeSource == "" {
		return fmt.Errorf("contracts: health event requires probe_source")
	}
	return nil
}

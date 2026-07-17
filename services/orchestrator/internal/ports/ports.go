// Package ports defines the orchestrator's seams to the outside world. Every
// dependency with side effects (telemetry, control-plane, infra scripts, active
// probes, wall clock) sits behind one of these interfaces so the policy engine
// and the reconcile loop are testable with mocks and the real adapters are
// swappable. See docs/orchestrator.md.
package ports

import (
	"context"
	"time"

	"github.com/caspervpn/contracts"
)

// TelemetryClient consumes the feedback loop (GET /v1/recommendations) and
// feeds it back with authoritative probe verdicts (POST /v1/health).
type TelemetryClient interface {
	// Recommendations returns the latest evaluation window payload.
	Recommendations(ctx context.Context) (contracts.Recommendations, error)
	// ReportHealth submits an authoritative HealthEvent from an own probe.
	ReportHealth(ctx context.Context, ev contracts.HealthEvent) error
}

// NodeFilter narrows ControlPlaneClient.ListNodes.
type NodeFilter struct {
	Status contracts.NodeStatus
	Role   contracts.NodeRole
	Region string
}

// ControlPlaneClient is the orchestrator's view of the control-plane node
// registry. Writes require an orchestrator-role service token.
type ControlPlaneClient interface {
	ListNodes(ctx context.Context, f NodeFilter) ([]contracts.Node, error)
	GetNode(ctx context.Context, id string) (contracts.Node, error)
	// UpdateNode PATCHes the full Node object (the control-plane replaces it).
	UpdateNode(ctx context.Context, n contracts.Node) (contracts.Node, error)
}

// Provisioner drives the node lifecycle (infra/scripts). Implementations MUST
// bound each call with a timeout and MUST NOT embed clouds, domains or IPs —
// everything flows in through parameters/env (anti-block rule 5).
//
// Note: the scripts themselves register/patch/retire the Node in the
// control-plane when CONTROL_PLANE_URL is set; the orchestrator verifies the
// result via ControlPlaneClient afterwards instead of duplicating the writes.
type Provisioner interface {
	// NodeUp provisions a fresh entry+exit pair in the given placement.
	NodeUp(ctx context.Context, region, cloud string) error
	// NodeRotate replaces the node's ephemeral entry IP and re-keys REALITY.
	NodeRotate(ctx context.Context, nodeID string) error
	// NodeDown drains and tears a node down.
	NodeDown(ctx context.Context, nodeID string) error
}

// Prober actively checks a node and renders an authoritative verdict. Real
// in-country probes are operator infrastructure; MVP ships a minimal TCP
// prober behind this seam.
type Prober interface {
	Probe(ctx context.Context, node contracts.Node) (contracts.HealthEvent, error)
}

// Clock abstracts time for deterministic tests.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production Clock.
type SystemClock struct{}

// Now returns the current wall-clock time.
func (SystemClock) Now() time.Time { return time.Now() }

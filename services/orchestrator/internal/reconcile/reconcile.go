// Package reconcile is the orchestrator's main loop: read telemetry
// recommendations, read the control-plane fleet state, confirm suspicions
// with own probes, ask the policy engine for a plan, then (only when
// DRY_RUN=false) execute it through the Provisioner and the control-plane.
//
// Execution order for a replacement is deliberately conservative: the
// replacement is provisioned and registered BEFORE the old node starts
// draining, and the old node is retired only after the drain grace period —
// old and new coexist, users migrate lazily via control-plane set rebuilds.
// Any mid-plan failure stops the cycle (no cascading half-done actions).
package reconcile

import (
	"context"
	"fmt"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/orchestrator/internal/policy"
	"github.com/caspervpn/orchestrator/internal/ports"
)

// Deps are the loop's seams — all mockable interfaces from ports.
type Deps struct {
	Telemetry ports.TelemetryClient
	CP        ports.ControlPlaneClient
	Prov      ports.Provisioner
	Prober    ports.Prober
	Clock     ports.Clock
	Logf      func(format string, args ...any)
}

// Options tune one loop instance.
type Options struct {
	// DryRun plans and logs but never executes. The safe default.
	DryRun bool
	// Thresholds are the policy knobs.
	Thresholds policy.Thresholds
	// ProbeEnabled runs confirmation probes for suspected nodes and reports
	// the verdicts to telemetry.
	ProbeEnabled bool
	// DefaultRegion/DefaultCloud place a replacement when the node being
	// replaced carries no placement of its own. From env only.
	DefaultRegion string
	DefaultCloud  string
	// Interval between cycles for Run.
	Interval time.Duration
}

// Report summarizes one cycle for logs and tests.
type Report struct {
	Plan     []policy.Action
	Executed int // side-effecting actions actually performed
	DryRun   bool
}

// Loop is one reconcile loop instance.
type Loop struct {
	deps Deps
	opts Options
}

// New builds a Loop. Deps must be fully populated (Prober may be nil when
// probing is disabled).
func New(deps Deps, opts Options) *Loop {
	if deps.Logf == nil {
		deps.Logf = func(string, ...any) {}
	}
	if deps.Clock == nil {
		deps.Clock = ports.SystemClock{}
	}
	return &Loop{deps: deps, opts: opts}
}

// Run cycles until ctx is done. Errors are logged, never fatal — the loop is
// the service.
func (l *Loop) Run(ctx context.Context) {
	interval := l.opts.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if rep, err := l.Cycle(ctx); err != nil {
			l.deps.Logf("reconcile: cycle failed: %v", err)
		} else {
			l.deps.Logf("reconcile: cycle done: %d planned, %d executed (dry_run=%v)",
				len(rep.Plan), rep.Executed, rep.DryRun)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Cycle performs one full observe → plan → (maybe) act pass.
func (l *Loop) Cycle(ctx context.Context) (Report, error) {
	now := l.deps.Clock.Now()

	// 1. Observe: fleet state is mandatory, recommendations are best-effort
	// (without them scheduled rotation and drain-retire still proceed).
	nodes, err := l.deps.CP.ListNodes(ctx, ports.NodeFilter{})
	if err != nil {
		return Report{}, fmt.Errorf("reconcile: fleet state unavailable: %w", err)
	}
	recs, err := l.deps.Telemetry.Recommendations(ctx)
	if err != nil {
		l.deps.Logf("reconcile: recommendations unavailable (%v) — planning without them", err)
		recs = contracts.Recommendations{}
	}

	// 2. Confirm: probe the nodes telemetry is suspicious about. Verdicts are
	// the anti-poisoning gate's second factor AND are fed back to telemetry.
	probes := l.confirmSuspects(ctx, recs, nodes)

	// 3. Plan (pure).
	plan := policy.Plan(policy.Input{Now: now, Recs: recs, Nodes: nodes, Probes: probes}, l.opts.Thresholds)
	for _, a := range plan {
		l.deps.Logf("reconcile: planned %-13s node=%s regions=%v reason=%s", a.Type, a.NodeID, a.Regions, a.Reason)
	}
	if l.opts.DryRun {
		return Report{Plan: plan, DryRun: true}, nil
	}

	// 4. Act. Stop at the first failure — a half-executed plan must not cascade.
	rep := Report{Plan: plan}
	byID := make(map[string]contracts.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, a := range plan {
		if a.Type == policy.ActionNoop {
			continue
		}
		if err := l.execute(ctx, a, byID[a.NodeID], now); err != nil {
			return rep, fmt.Errorf("reconcile: %s %s: %w (stopping this cycle)", a.Type, a.NodeID, err)
		}
		if a.Type != policy.ActionMarkDegraded {
			rep.Executed++
		}
	}
	return rep, nil
}

// confirmSuspects probes nodes named in block recommendations. Only fresh
// own-probe verdicts can upgrade a corroborated field verdict to action.
func (l *Loop) confirmSuspects(ctx context.Context, recs contracts.Recommendations, nodes []contracts.Node) map[string]contracts.HealthEvent {
	if !l.opts.ProbeEnabled || l.deps.Prober == nil {
		return nil
	}
	byID := make(map[string]contracts.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	out := make(map[string]contracts.HealthEvent)
	for _, b := range recs.NodeBlocks {
		node, ok := byID[b.NodeID]
		if !ok || out[b.NodeID].NodeID != "" {
			continue
		}
		ev, err := l.deps.Prober.Probe(ctx, node)
		if err != nil {
			l.deps.Logf("reconcile: probe %s failed: %v", b.NodeID, err)
			continue
		}
		out[b.NodeID] = ev
		// Feed the authoritative verdict back to telemetry (best-effort).
		// Suppressed in dry-run: reporting influences telemetry's future
		// recommendations, so it counts as a side effect. Probing itself is
		// read-only observation and still runs, so the plan stays realistic.
		if l.opts.DryRun {
			l.deps.Logf("reconcile: dry-run — probe verdict for %s (%s) NOT reported to telemetry", b.NodeID, ev.Status)
			continue
		}
		if err := l.deps.Telemetry.ReportHealth(ctx, ev); err != nil {
			l.deps.Logf("reconcile: report health for %s failed: %v", b.NodeID, err)
		}
	}
	return out
}

// execute performs one side-effecting action.
func (l *Loop) execute(ctx context.Context, a policy.Action, node contracts.Node, now time.Time) error {
	switch a.Type {
	case policy.ActionMarkDegraded:
		node.Status = contracts.NodeStatusDegraded
		_, err := l.deps.CP.UpdateNode(ctx, node)
		return err

	case policy.ActionRotate:
		// node_rotate.sh replaces the ephemeral entry VM, re-keys REALITY and
		// PATCHes the Node in the control-plane itself. We verify afterwards.
		oldIP := node.EntryIP
		if err := l.deps.Prov.NodeRotate(ctx, a.NodeID); err != nil {
			return err
		}
		after, err := l.deps.CP.GetNode(ctx, a.NodeID)
		if err != nil {
			return fmt.Errorf("rotate succeeded but verification failed: %w", err)
		}
		if after.EntryIP == oldIP {
			l.deps.Logf("reconcile: WARNING rotate %s: entry_ip unchanged in control-plane (%s) — check CONTROL_PLANE_URL on the script side", a.NodeID, oldIP)
		}
		return nil

	case policy.ActionReplace:
		// Order matters: replacement FIRST (node_up.sh registers it in the
		// control-plane), old node starts draining only after that succeeds.
		region, cloud := node.Region, node.Cloud
		if region == "" {
			region = l.opts.DefaultRegion
		}
		if cloud == "" {
			cloud = l.opts.DefaultCloud
		}
		if err := l.deps.Prov.NodeUp(ctx, region, cloud); err != nil {
			return err // old node untouched — nothing to roll back
		}
		node.Status = contracts.NodeStatusDraining
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		node.Labels[policy.DrainStartedLabel] = now.UTC().Format(time.RFC3339)
		_, err := l.deps.CP.UpdateNode(ctx, node)
		return err

	case policy.ActionRetire:
		// node_down.sh drains, retires the Node record and destroys the infra.
		if err := l.deps.Prov.NodeDown(ctx, a.NodeID); err != nil {
			return err
		}
		// Belt and braces: make sure the registry agrees.
		after, err := l.deps.CP.GetNode(ctx, a.NodeID)
		if err == nil && after.Status != contracts.NodeStatusRetired {
			after.Status = contracts.NodeStatusRetired
			if _, err := l.deps.CP.UpdateNode(ctx, after); err != nil {
				return fmt.Errorf("node down succeeded but retire mark failed: %w", err)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown action %q", a.Type)
}

package usecase

import (
	"context"
	"errors"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
)

// Verdict thresholds over one aggregate window (ratios of total observations).
const (
	blockedRatioThreshold  = 0.5 // >= this share of block-class signals => blocked
	degradedRatioThreshold = 0.3 // >= this share of failures => degraded
)

// blockClassSignals are the signal types that indicate active censorship of a
// node (as opposed to plain flakiness). Probing counts — it precedes a block.
var blockClassSignals = map[contracts.SignalType]bool{
	contracts.SignalReset:         true,
	contracts.SignalTimeout:       true,
	contracts.SignalDPIBlock:      true,
	contracts.SignalDNSPoison:     true,
	contracts.SignalHandshakeFail: true,
	contracts.SignalActiveProbe:   true,
}

// SignalService consumes FieldSignal aggregates from telemetry and marks nodes
// degraded/blocked — the feedback loop that turns "where it got blocked" into
// automatic fleet changes. It never sees per-user identity.
type SignalService struct {
	nodes   domain.NodeRepo
	signals domain.SignalRepo
	nodeSvc *NodeService
}

// NewSignalService wires a SignalService. It reuses NodeService for status
// transitions so history + rebuild-enqueue stay in one place.
func NewSignalService(nodes domain.NodeRepo, signals domain.SignalRepo, nodeSvc *NodeService) *SignalService {
	return &SignalService{nodes: nodes, signals: signals, nodeSvc: nodeSvc}
}

// Ingest records aggregates and applies the worst per-node verdict, returning
// the status changes it made.
func (s *SignalService) Ingest(ctx context.Context, aggregates []domain.SignalAggregate, actor string) ([]domain.NodeStatusChange, error) {
	worst := map[string]domain.Verdict{}
	for _, a := range aggregates {
		if a.Total <= 0 {
			continue
		}
		blockSignals, verdict := classify(a)
		if err := s.signals.AddAggregate(ctx, a, blockSignals, verdict); err != nil {
			return nil, err
		}
		if rank(verdict) > rank(worst[a.NodeID]) {
			worst[a.NodeID] = verdict
		}
	}

	var changes []domain.NodeStatusChange
	for nodeID, verdict := range worst {
		change, err := s.applyVerdict(ctx, nodeID, verdict, actor)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				continue // node gone; skip
			}
			return nil, err
		}
		if change != nil {
			changes = append(changes, *change)
		}
	}
	return changes, nil
}

// applyVerdict maps a verdict to a node status transition, guarding against
// clobbering operator-controlled states (provisioning/draining/retired) and
// against auto-unblocking a blocked node from a single healthy window.
func (s *SignalService) applyVerdict(ctx context.Context, nodeID string, verdict domain.Verdict, actor string) (*domain.NodeStatusChange, error) {
	node, err := s.nodes.Get(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	cur := node.Status
	// Only field-driven states are eligible for automatic movement.
	if cur != contracts.NodeStatusActive && cur != contracts.NodeStatusDegraded && cur != contracts.NodeStatusBlocked {
		return nil, nil
	}

	var target contracts.NodeStatus
	switch verdict {
	case domain.VerdictBlocked:
		target = contracts.NodeStatusBlocked
	case domain.VerdictDegraded:
		if cur == contracts.NodeStatusBlocked {
			return nil, nil // don't downgrade a block on one window
		}
		target = contracts.NodeStatusDegraded
	default: // healthy
		if cur == contracts.NodeStatusBlocked {
			return nil, nil // recovery from block is an orchestrator decision
		}
		target = contracts.NodeStatusActive
	}
	if target == cur {
		return nil, nil
	}

	from, changed, err := s.nodeSvc.setStatusIfChanged(ctx, nodeID, target, "field-signal:"+string(verdict), actor)
	if err != nil || !changed {
		return nil, err
	}
	return &domain.NodeStatusChange{NodeID: nodeID, From: from, To: target}, nil
}

// classify returns the block-class signal count and the verdict for an aggregate.
func classify(a domain.SignalAggregate) (blockSignals int, verdict domain.Verdict) {
	ok := a.BySignal[contracts.SignalOK]
	for t, c := range a.BySignal {
		if blockClassSignals[t] {
			blockSignals += c
		}
	}
	failures := a.Total - ok
	if failures < 0 {
		failures = 0
	}
	blockRatio := float64(blockSignals) / float64(a.Total)
	failRatio := float64(failures) / float64(a.Total)
	switch {
	case blockRatio >= blockedRatioThreshold:
		return blockSignals, domain.VerdictBlocked
	case failRatio >= degradedRatioThreshold:
		return blockSignals, domain.VerdictDegraded
	default:
		return blockSignals, domain.VerdictHealthy
	}
}

func rank(v domain.Verdict) int {
	switch v {
	case domain.VerdictBlocked:
		return 2
	case domain.VerdictDegraded:
		return 1
	default:
		return 0
	}
}

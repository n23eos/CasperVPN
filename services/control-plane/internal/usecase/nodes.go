package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/secret"
)

// NodeService owns the dynamic node registry. No node, IP, or domain is ever
// hardcoded — everything flows through the repo from data.
type NodeService struct {
	nodes     domain.NodeRepo
	rotations domain.RotationRepo
	queue     domain.RebuildQueue
	now       func() time.Time
}

// NewNodeService wires a NodeService.
func NewNodeService(nodes domain.NodeRepo, rotations domain.RotationRepo, queue domain.RebuildQueue) *NodeService {
	return &NodeService{nodes: nodes, rotations: rotations, queue: queue, now: time.Now}
}

// Register adds a node to the registry (called by the orchestrator/infra).
func (s *NodeService) Register(ctx context.Context, n contracts.Node, actor string) (contracts.Node, error) {
	if n.ID == "" {
		id, err := secret.NodeID()
		if err != nil {
			return contracts.Node{}, err
		}
		n.ID = id
	}
	if n.Status == "" {
		n.Status = contracts.NodeStatusProvisioning
	}
	// Registration never creates a SERVING node: a fresh node must come up
	// provisioning and be promoted separately through the guarded activation flow,
	// never straight to active. A POST with status:active would let a caller skip
	// that promotion, so reject it explicitly rather than silently downgrade.
	if n.Status == contracts.NodeStatusActive {
		return contracts.Node{}, fmt.Errorf("%w: register creates a provisioning node; reach active via the guarded activation flow", domain.ErrConflict)
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = s.now()
	}
	if err := n.Validate(); err != nil {
		return contracts.Node{}, fmt.Errorf("%w: %s", domain.ErrValidation, err)
	}
	if err := s.nodes.Create(ctx, n); err != nil {
		return contracts.Node{}, err
	}
	if err := s.recordRotation(ctx, n.ID, nil, n.Status, "register", n.EntryIP, actor); err != nil {
		return contracts.Node{}, err
	}
	// A freshly registered node is always provisioning (active is rejected above),
	// so it never enters the active set here — no rebuild enqueue is needed.
	return n, nil
}

// Get returns a node by id.
func (s *NodeService) Get(ctx context.Context, id string) (contracts.Node, error) {
	return s.nodes.Get(ctx, id)
}

// List returns a page of nodes matching the filter.
func (s *NodeService) List(ctx context.Context, f domain.NodeFilter) ([]contracts.Node, string, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	return s.nodes.List(ctx, f)
}

// Update replaces a node's mutable representation (PATCH = full body per the
// frozen OpenAPI). Immutable id/created_at are preserved from the stored node.
func (s *NodeService) Update(ctx context.Context, id string, n contracts.Node, actor string) (contracts.Node, error) {
	existing, err := s.nodes.Get(ctx, id)
	if err != nil {
		return contracts.Node{}, err
	}
	n.ID = existing.ID
	n.CreatedAt = existing.CreatedAt
	if err := n.Validate(); err != nil {
		return contracts.Node{}, fmt.Errorf("%w: %s", domain.ErrValidation, err)
	}
	if err := s.nodes.Update(ctx, n); err != nil {
		return contracts.Node{}, err
	}
	if existing.Status != n.Status {
		prev := existing.Status
		if err := s.recordRotation(ctx, id, &prev, n.Status, "update", n.EntryIP, actor); err != nil {
			return contracts.Node{}, err
		}
	}
	// Any registry change to an active-set-relevant node reshapes users' sets.
	s.queue.EnqueueNode(id)
	return n, nil
}

// Rotate advertises a new ingress IP for a node (fast entry rotation). Users get
// the new address on the next rebuild.
func (s *NodeService) Rotate(ctx context.Context, id, newEntryIP, reason, actor string) (contracts.Node, error) {
	node, err := s.nodes.Get(ctx, id)
	if err != nil {
		return contracts.Node{}, err
	}
	if newEntryIP != "" {
		if err := s.nodes.SetEntryIP(ctx, id, newEntryIP); err != nil {
			return contracts.Node{}, err
		}
		node.EntryIP = newEntryIP
	}
	if reason == "" {
		reason = "rotate"
	}
	prev := node.Status
	if err := s.recordRotation(ctx, id, &prev, node.Status, reason, node.EntryIP, actor); err != nil {
		return contracts.Node{}, err
	}
	s.queue.EnqueueNode(id)
	return node, nil
}

// Retire drains and tears down a node (DELETE). Affected users are rebuilt onto
// the remaining fleet automatically.
func (s *NodeService) Retire(ctx context.Context, id, actor string) error {
	prev, err := s.nodes.SetStatus(ctx, id, contracts.NodeStatusRetired)
	if err != nil {
		return err
	}
	if err := s.recordRotation(ctx, id, &prev, contracts.NodeStatusRetired, "retire", "", actor); err != nil {
		return err
	}
	s.queue.EnqueueNode(id)
	return nil
}

// History returns recent rotation records for a node.
func (s *NodeService) History(ctx context.Context, id string, limit int) ([]domain.RotationRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.rotations.ListNodeRotations(ctx, id, limit)
}

func (s *NodeService) recordRotation(ctx context.Context, nodeID string, from *contracts.NodeStatus, to contracts.NodeStatus, reason, entryIP, actor string) error {
	id, err := secret.UUIDv4()
	if err != nil {
		return err
	}
	rec := domain.RotationRecord{
		ID: id, NodeID: nodeID, FromStatus: from, ToStatus: to,
		Reason: reason, EntryIP: entryIP, Actor: actor, CreatedAt: s.now(),
	}
	if err := s.rotations.AddNodeRotation(ctx, rec); err != nil {
		return fmt.Errorf("record rotation: %w", err)
	}
	return nil
}

// setStatusIfChanged transitions a node's status, records history, and enqueues
// a rebuild — shared by the signal ingest path. Returns whether it changed.
func (s *NodeService) setStatusIfChanged(ctx context.Context, id string, to contracts.NodeStatus, reason, actor string) (from contracts.NodeStatus, changed bool, err error) {
	prev, err := s.nodes.SetStatus(ctx, id, to)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", false, err
		}
		return "", false, err
	}
	if prev == to {
		return prev, false, nil
	}
	if err := s.recordRotation(ctx, id, &prev, to, reason, "", actor); err != nil {
		return prev, false, err
	}
	s.queue.EnqueueNode(id)
	return prev, true, nil
}

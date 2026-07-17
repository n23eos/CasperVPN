package usecase

import (
	"context"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
)

// AllowListService answers the REALITY allow-list a node must admit. It gates
// the read on the node existing (so a bad id 404s), then returns every eligible
// user's (uuid, short_id) plus a revision digest of the set. Assignment is
// currently all-active-users to all-active-nodes, so the list is node-independent
// in content; the node id is validated but does not scope the set (yet).
type AllowListService struct {
	nodes domain.NodeRepo
	allow domain.AllowListRepo
}

// NewAllowListService wires the service.
func NewAllowListService(nodes domain.NodeRepo, allow domain.AllowListRepo) *AllowListService {
	return &AllowListService{nodes: nodes, allow: allow}
}

// ForNode returns the allow-list for a node, or domain.ErrNotFound if the node
// does not exist.
func (s *AllowListService) ForNode(ctx context.Context, nodeID string) (contracts.NodeRealityUsers, error) {
	if _, err := s.nodes.Get(ctx, nodeID); err != nil {
		return contracts.NodeRealityUsers{}, err
	}
	users, err := s.allow.EligibleRealityUsers(ctx)
	if err != nil {
		return contracts.NodeRealityUsers{}, err
	}
	if users == nil {
		users = []contracts.RealityUser{}
	}
	return contracts.NodeRealityUsers{
		Revision: contracts.RealityUsersRevision(users),
		Users:    users,
	}, nil
}

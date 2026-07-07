package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
)

// BundleService builds the per-user Node×Transport set — the structured data the
// subscription service (Agent C) renders into a subscription. The node list is
// DYNAMIC (active nodes from the DB); nothing is hardcoded. Only THIS user's
// personal secrets travel in the bundle (per-user isolation).
type BundleService struct {
	users domain.UserRepo
	nodes domain.NodeRepo
	sets  domain.SetRepo
	now   func() time.Time
}

// NewBundleService wires a BundleService.
func NewBundleService(users domain.UserRepo, nodes domain.NodeRepo, sets domain.SetRepo) *BundleService {
	return &BundleService{users: users, nodes: nodes, sets: sets, now: time.Now}
}

// Build recomputes a user's active set from the current fleet and persists it
// (revision bumped, cache marked fresh). Used on demand and by the rebuild queue.
func (s *BundleService) Build(ctx context.Context, userID string) (domain.SubscriptionSet, error) {
	user, err := s.users.Get(ctx, userID)
	if err != nil {
		return domain.SubscriptionSet{}, err
	}
	nodes, err := s.nodes.ListActive(ctx)
	if err != nil {
		return domain.SubscriptionSet{}, err
	}
	nodeIDs := make([]string, 0, len(nodes))
	for _, n := range nodes {
		nodeIDs = append(nodeIDs, n.ID)
	}

	prevRev := int64(0)
	if prev, err := s.sets.Get(ctx, userID); err == nil {
		prevRev = prev.Revision
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.SubscriptionSet{}, err
	}

	set := domain.SubscriptionSet{
		UserID:   userID,
		Revision: prevRev + 1,
		NodeIDs:  nodeIDs,
		Bundle: contracts.SubscriptionBundle{
			User:        user,
			Nodes:       nodes,
			GeneratedAt: s.now(),
		},
		Dirty:       false,
		GeneratedAt: s.now(),
	}
	if err := s.sets.Upsert(ctx, set); err != nil {
		return domain.SubscriptionSet{}, err
	}
	return set, nil
}

// GetForUser returns the cached set, rebuilding it when missing or stale. This
// is the data source for Agent C's GET subscription-set.
func (s *BundleService) GetForUser(ctx context.Context, userID string) (domain.SubscriptionSet, error) {
	set, err := s.sets.Get(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return s.Build(ctx, userID)
		}
		return domain.SubscriptionSet{}, err
	}
	if set.Dirty {
		return s.Build(ctx, userID)
	}
	return set, nil
}

package usecase

import (
	"context"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
)

// In-memory fakes for unit tests. They implement the domain ports without a DB.

type fakeNodeRepo struct {
	nodes map[string]contracts.Node
}

func newFakeNodeRepo() *fakeNodeRepo { return &fakeNodeRepo{nodes: map[string]contracts.Node{}} }

func (r *fakeNodeRepo) Create(_ context.Context, n contracts.Node) error {
	if _, ok := r.nodes[n.ID]; ok {
		return domain.ErrConflict
	}
	r.nodes[n.ID] = n
	return nil
}

func (r *fakeNodeRepo) Get(_ context.Context, id string) (contracts.Node, error) {
	n, ok := r.nodes[id]
	if !ok {
		return contracts.Node{}, domain.ErrNotFound
	}
	return n, nil
}

func (r *fakeNodeRepo) List(_ context.Context, f domain.NodeFilter) ([]contracts.Node, string, error) {
	var out []contracts.Node
	for _, n := range r.nodes {
		if f.Status != "" && n.Status != f.Status {
			continue
		}
		if f.Role != "" && n.Role != f.Role {
			continue
		}
		if f.Region != "" && n.Region != f.Region {
			continue
		}
		out = append(out, n)
	}
	return out, "", nil
}

func (r *fakeNodeRepo) Update(_ context.Context, n contracts.Node) error {
	if _, ok := r.nodes[n.ID]; !ok {
		return domain.ErrNotFound
	}
	r.nodes[n.ID] = n
	return nil
}

func (r *fakeNodeRepo) SetStatus(_ context.Context, id string, status contracts.NodeStatus) (contracts.NodeStatus, error) {
	n, ok := r.nodes[id]
	if !ok {
		return "", domain.ErrNotFound
	}
	prev := n.Status
	n.Status = status
	r.nodes[id] = n
	return prev, nil
}

func (r *fakeNodeRepo) Demote(_ context.Context, id string) (contracts.NodeStatus, error) {
	n, ok := r.nodes[id]
	if !ok {
		return "", domain.ErrNotFound
	}
	prev := n.Status
	switch prev {
	case contracts.NodeStatusDraining, contracts.NodeStatusRetired:
		return prev, domain.ErrConflict
	}
	n.Status = contracts.NodeStatusProvisioning
	r.nodes[id] = n
	return prev, nil
}

func (r *fakeNodeRepo) SetEntryIP(_ context.Context, id, entryIP string) error {
	n, ok := r.nodes[id]
	if !ok {
		return domain.ErrNotFound
	}
	n.EntryIP = entryIP
	r.nodes[id] = n
	return nil
}

func (r *fakeNodeRepo) ListActive(_ context.Context) ([]contracts.Node, error) {
	var out []contracts.Node
	for _, n := range r.nodes {
		if n.Status == contracts.NodeStatusActive {
			out = append(out, n)
		}
	}
	return out, nil
}

type fakeUserRepo struct {
	users map[string]contracts.User
}

func newFakeUserRepo() *fakeUserRepo { return &fakeUserRepo{users: map[string]contracts.User{}} }

func (r *fakeUserRepo) Create(_ context.Context, u contracts.User) error {
	if _, ok := r.users[u.ID]; ok {
		return domain.ErrConflict
	}
	r.users[u.ID] = u
	return nil
}

func (r *fakeUserRepo) Get(_ context.Context, id string) (contracts.User, error) {
	u, ok := r.users[id]
	if !ok {
		return contracts.User{}, domain.ErrNotFound
	}
	return u, nil
}

func (r *fakeUserRepo) Update(_ context.Context, u contracts.User) error {
	if _, ok := r.users[u.ID]; !ok {
		return domain.ErrNotFound
	}
	r.users[u.ID] = u
	return nil
}

func (r *fakeUserRepo) RotateSecrets(_ context.Context, id, shortID, uuid, privKey string) (contracts.User, error) {
	u, ok := r.users[id]
	if !ok {
		return contracts.User{}, domain.ErrNotFound
	}
	u.RealityShortID = shortID
	u.UUID = uuid
	u.PrivateKey = privKey
	r.users[id] = u
	return u, nil
}

func (r *fakeUserRepo) AllActiveIDs(_ context.Context) ([]string, error) {
	var out []string
	for id, u := range r.users {
		if u.Status == contracts.UserStatusActive {
			out = append(out, id)
		}
	}
	return out, nil
}

type fakeSubRepo struct {
	subs   map[string]contracts.Subscription
	hashes map[string]string
	prefix map[string]string
}

func newFakeSubRepo() *fakeSubRepo {
	return &fakeSubRepo{subs: map[string]contracts.Subscription{}, hashes: map[string]string{}, prefix: map[string]string{}}
}

func (r *fakeSubRepo) Create(_ context.Context, s contracts.Subscription, tokenHash, tokenPrefix string) error {
	stored := s
	stored.Token = "" // never persist plaintext
	r.subs[s.ID] = stored
	r.hashes[s.ID] = tokenHash
	r.prefix[s.ID] = tokenPrefix
	return nil
}

func (r *fakeSubRepo) Get(_ context.Context, id string) (contracts.Subscription, error) {
	s, ok := r.subs[id]
	if !ok {
		return contracts.Subscription{}, domain.ErrNotFound
	}
	return s, nil
}

func (r *fakeSubRepo) Update(_ context.Context, s contracts.Subscription) error {
	if _, ok := r.subs[s.ID]; !ok {
		return domain.ErrNotFound
	}
	stored := s
	stored.Token = "" // never persist plaintext
	r.subs[s.ID] = stored
	return nil
}

func (r *fakeSubRepo) UpdateTokenHash(_ context.Context, id, tokenHash, tokenPrefix string) error {
	if _, ok := r.subs[id]; !ok {
		return domain.ErrNotFound
	}
	r.hashes[id] = tokenHash
	r.prefix[id] = tokenPrefix
	return nil
}

// fakeRevoker records downstream revocations/registrations (TZ-token-revocation).
type fakeRevoker struct {
	revokedUsers []string
	revokedSubs  []string
	registered   []struct{ token, userID, subID string }
	fail         bool
}

func newFakeRevoker() *fakeRevoker { return &fakeRevoker{} }

func (f *fakeRevoker) RevokeUser(_ context.Context, userID string) error {
	if f.fail {
		return context.DeadlineExceeded
	}
	f.revokedUsers = append(f.revokedUsers, userID)
	return nil
}

func (f *fakeRevoker) RevokeSubscription(_ context.Context, subID string) error {
	if f.fail {
		return context.DeadlineExceeded
	}
	f.revokedSubs = append(f.revokedSubs, subID)
	return nil
}

func (f *fakeRevoker) RegisterToken(_ context.Context, token, userID, subID string) error {
	if f.fail {
		return context.DeadlineExceeded
	}
	f.registered = append(f.registered, struct{ token, userID, subID string }{token, userID, subID})
	return nil
}

type fakeRotationRepo struct {
	nodeRecs []domain.RotationRecord
	userRecs []domain.UserSecretRotation
}

func newFakeRotationRepo() *fakeRotationRepo { return &fakeRotationRepo{} }

func (r *fakeRotationRepo) AddNodeRotation(_ context.Context, rec domain.RotationRecord) error {
	r.nodeRecs = append(r.nodeRecs, rec)
	return nil
}

func (r *fakeRotationRepo) ListNodeRotations(_ context.Context, nodeID string, limit int) ([]domain.RotationRecord, error) {
	var out []domain.RotationRecord
	for i := len(r.nodeRecs) - 1; i >= 0 && len(out) < limit; i-- {
		if r.nodeRecs[i].NodeID == nodeID {
			out = append(out, r.nodeRecs[i])
		}
	}
	return out, nil
}

func (r *fakeRotationRepo) AddUserSecretRotation(_ context.Context, rec domain.UserSecretRotation) error {
	r.userRecs = append(r.userRecs, rec)
	return nil
}

type fakeSetRepo struct {
	sets map[string]domain.SubscriptionSet
}

func newFakeSetRepo() *fakeSetRepo { return &fakeSetRepo{sets: map[string]domain.SubscriptionSet{}} }

func (r *fakeSetRepo) Get(_ context.Context, userID string) (domain.SubscriptionSet, error) {
	s, ok := r.sets[userID]
	if !ok {
		return domain.SubscriptionSet{}, domain.ErrNotFound
	}
	return s, nil
}

func (r *fakeSetRepo) Upsert(_ context.Context, s domain.SubscriptionSet) error {
	r.sets[s.UserID] = s
	return nil
}

func (r *fakeSetRepo) UserIDsByNode(_ context.Context, nodeID string) ([]string, error) {
	var out []string
	for id, s := range r.sets {
		for _, nid := range s.NodeIDs {
			if nid == nodeID {
				out = append(out, id)
				break
			}
		}
	}
	return out, nil
}

func (r *fakeSetRepo) MarkDirtyByNode(_ context.Context, nodeID string) error {
	for id, s := range r.sets {
		for _, nid := range s.NodeIDs {
			if nid == nodeID {
				s.Dirty = true
				r.sets[id] = s
				break
			}
		}
	}
	return nil
}

type fakeSignalRepo struct {
	aggregates []domain.SignalAggregate
	verdicts   []domain.Verdict
}

func newFakeSignalRepo() *fakeSignalRepo { return &fakeSignalRepo{} }

func (r *fakeSignalRepo) AddAggregate(_ context.Context, a domain.SignalAggregate, _ int, verdict domain.Verdict) error {
	r.aggregates = append(r.aggregates, a)
	r.verdicts = append(r.verdicts, verdict)
	return nil
}

type fakeQueue struct {
	nodes []string
	users []string
}

func newFakeQueue() *fakeQueue { return &fakeQueue{} }

func (q *fakeQueue) EnqueueNode(nodeID string) { q.nodes = append(q.nodes, nodeID) }
func (q *fakeQueue) EnqueueUser(userID string) { q.users = append(q.users, userID) }

// Package memory provides in-memory implementations of the domain repositories.
// It backs the HTTP contract tests (no database needed) and a no-DB local run.
// It is intentionally simple and NOT for production traffic.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
)

// Nodes is an in-memory NodeRepo.
type Nodes struct {
	mu    sync.Mutex
	nodes map[string]contracts.Node
}

// NewNodes builds an empty node store.
func NewNodes() *Nodes { return &Nodes{nodes: map[string]contracts.Node{}} }

func (r *Nodes) Create(_ context.Context, n contracts.Node) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nodes[n.ID]; ok {
		return domain.ErrConflict
	}
	r.nodes[n.ID] = n
	return nil
}

func (r *Nodes) Get(_ context.Context, id string) (contracts.Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return contracts.Node{}, domain.ErrNotFound
	}
	return n, nil
}

func (r *Nodes) List(_ context.Context, f domain.NodeFilter) ([]contracts.Node, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *Nodes) Update(_ context.Context, n contracts.Node) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nodes[n.ID]; !ok {
		return domain.ErrNotFound
	}
	r.nodes[n.ID] = n
	return nil
}

func (r *Nodes) SetStatus(_ context.Context, id string, status contracts.NodeStatus) (contracts.NodeStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return "", domain.ErrNotFound
	}
	prev := n.Status
	n.Status = status
	r.nodes[id] = n
	return prev, nil
}

func (r *Nodes) Demote(_ context.Context, id string) (contracts.NodeStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *Nodes) SetEntryIP(_ context.Context, id, entryIP string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return domain.ErrNotFound
	}
	n.EntryIP = entryIP
	r.nodes[id] = n
	return nil
}

func (r *Nodes) ListActive(_ context.Context) ([]contracts.Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []contracts.Node
	for _, n := range r.nodes {
		if n.Status == contracts.NodeStatusActive {
			out = append(out, n)
		}
	}
	return out, nil
}

// Users is an in-memory UserRepo.
type Users struct {
	mu    sync.Mutex
	users map[string]contracts.User
}

// NewUsers builds an empty user store.
func NewUsers() *Users { return &Users{users: map[string]contracts.User{}} }

func (r *Users) Create(_ context.Context, u contracts.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[u.ID]; ok {
		return domain.ErrConflict
	}
	r.users[u.ID] = u
	return nil
}

func (r *Users) Get(_ context.Context, id string) (contracts.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return contracts.User{}, domain.ErrNotFound
	}
	return u, nil
}

func (r *Users) Update(_ context.Context, u contracts.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[u.ID]; !ok {
		return domain.ErrNotFound
	}
	r.users[u.ID] = u
	return nil
}

func (r *Users) RotateSecrets(_ context.Context, id, shortID, uuid, privKey string) (contracts.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *Users) AllActiveIDs(_ context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for id, u := range r.users {
		if u.Status == contracts.UserStatusActive {
			out = append(out, id)
		}
	}
	return out, nil
}

// Subscriptions is an in-memory SubscriptionRepo.
type Subscriptions struct {
	mu     sync.Mutex
	subs   map[string]contracts.Subscription
	hashes map[string]string
	users  *Users // for CreateAndLink's atomic create+link (wire with WithUsers)
}

// NewSubscriptions builds an empty subscription store.
func NewSubscriptions() *Subscriptions {
	return &Subscriptions{subs: map[string]contracts.Subscription{}, hashes: map[string]string{}}
}

// WithUsers wires the user store so CreateAndLink can link atomically. Returns the
// store for chaining. Process-local only — NOT cross-instance safe (see docs/billing).
func (r *Subscriptions) WithUsers(u *Users) *Subscriptions {
	r.users = u
	return r
}

// CreateAndLink mirrors the Postgres seam: insert the subscription and link it onto
// the user atomically, rejecting with ErrConflict if the user already has one.
// Holds the user lock then the subscription lock (a single, consistent order; no
// other op takes both, so no deadlock).
func (r *Subscriptions) CreateAndLink(_ context.Context, s contracts.Subscription, tokenHash, _, userID string) error {
	if r.users == nil {
		return domain.ErrValidation // misconfigured store; wire WithUsers
	}
	r.users.mu.Lock()
	defer r.users.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()

	u, ok := r.users.users[userID]
	if !ok {
		return domain.ErrNotFound
	}
	if u.SubscriptionID != nil && *u.SubscriptionID != "" {
		return domain.ErrConflict
	}
	stored := s
	stored.Token = ""
	r.subs[s.ID] = stored
	r.hashes[s.ID] = tokenHash
	subID := s.ID
	u.SubscriptionID = &subID
	u.UpdatedAt = s.UpdatedAt
	r.users.users[userID] = u
	return nil
}

func (r *Subscriptions) Create(_ context.Context, s contracts.Subscription, tokenHash, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored := s
	stored.Token = ""
	r.subs[s.ID] = stored
	r.hashes[s.ID] = tokenHash
	return nil
}

func (r *Subscriptions) Get(_ context.Context, id string) (contracts.Subscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.subs[id]
	if !ok {
		return contracts.Subscription{}, domain.ErrNotFound
	}
	return s, nil
}

func (r *Subscriptions) Update(_ context.Context, s contracts.Subscription) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.subs[s.ID]; !ok {
		return domain.ErrNotFound
	}
	stored := s
	stored.Token = "" // never persist plaintext
	r.subs[s.ID] = stored
	return nil
}

func (r *Subscriptions) UpdateTokenHash(_ context.Context, id, tokenHash, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.subs[id]; !ok {
		return domain.ErrNotFound
	}
	r.hashes[id] = tokenHash
	return nil
}

// Rotations is an in-memory RotationRepo.
type Rotations struct {
	mu       sync.Mutex
	nodeRecs []domain.RotationRecord
	userRecs []domain.UserSecretRotation
}

// NewRotations builds an empty rotation store.
func NewRotations() *Rotations { return &Rotations{} }

func (r *Rotations) AddNodeRotation(_ context.Context, rec domain.RotationRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodeRecs = append(r.nodeRecs, rec)
	return nil
}

func (r *Rotations) ListNodeRotations(_ context.Context, nodeID string, limit int) ([]domain.RotationRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.RotationRecord
	for i := len(r.nodeRecs) - 1; i >= 0 && len(out) < limit; i-- {
		if r.nodeRecs[i].NodeID == nodeID {
			out = append(out, r.nodeRecs[i])
		}
	}
	return out, nil
}

func (r *Rotations) AddUserSecretRotation(_ context.Context, rec domain.UserSecretRotation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.userRecs = append(r.userRecs, rec)
	return nil
}

// Sets is an in-memory SetRepo.
type Sets struct {
	mu   sync.Mutex
	sets map[string]domain.SubscriptionSet
}

// NewSets builds an empty set store.
func NewSets() *Sets { return &Sets{sets: map[string]domain.SubscriptionSet{}} }

func (r *Sets) Get(_ context.Context, userID string) (domain.SubscriptionSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sets[userID]
	if !ok {
		return domain.SubscriptionSet{}, domain.ErrNotFound
	}
	return s, nil
}

func (r *Sets) Upsert(_ context.Context, s domain.SubscriptionSet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sets[s.UserID] = s
	return nil
}

func (r *Sets) UserIDsByNode(_ context.Context, nodeID string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *Sets) MarkDirtyByNode(_ context.Context, nodeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
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

// Signals is an in-memory SignalRepo.
type Signals struct {
	mu         sync.Mutex
	aggregates []domain.SignalAggregate
}

// NewSignals builds an empty signal store.
func NewSignals() *Signals { return &Signals{} }

func (r *Signals) AddAggregate(_ context.Context, a domain.SignalAggregate, _ int, _ domain.Verdict) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aggregates = append(r.aggregates, a)
	return nil
}

// NoopQueue is a RebuildQueue that records enqueues but does no work — for tests.
type NoopQueue struct {
	mu    sync.Mutex
	Nodes []string
	Users []string
}

// NewNoopQueue builds a NoopQueue.
func NewNoopQueue() *NoopQueue { return &NoopQueue{} }

func (q *NoopQueue) EnqueueNode(id string) { q.mu.Lock(); q.Nodes = append(q.Nodes, id); q.mu.Unlock() }
func (q *NoopQueue) EnqueueUser(id string) { q.mu.Lock(); q.Users = append(q.Users, id); q.mu.Unlock() }

// AllowList is an in-memory AllowListRepo. It joins Users and Subscriptions the
// same way the Postgres query does, applying the exact eligibility predicate.
type AllowList struct {
	users *Users
	subs  *Subscriptions
	now   func() time.Time
}

// NewAllowList builds an AllowList over the given user + subscription stores.
func NewAllowList(users *Users, subs *Subscriptions) *AllowList {
	return &AllowList{users: users, subs: subs, now: time.Now}
}

func (a *AllowList) EligibleRealityUsers(_ context.Context) ([]contracts.RealityUser, error) {
	a.users.mu.Lock()
	usersCopy := make([]contracts.User, 0, len(a.users.users))
	for _, u := range a.users.users {
		usersCopy = append(usersCopy, u)
	}
	a.users.mu.Unlock()

	now := a.now()
	var out []contracts.RealityUser
	for _, u := range usersCopy {
		if u.Status != contracts.UserStatusActive || u.SubscriptionID == nil {
			continue
		}
		a.subs.mu.Lock()
		sub, ok := a.subs.subs[*u.SubscriptionID]
		a.subs.mu.Unlock()
		if !ok || !servable(sub.Status) {
			continue
		}
		if sub.ExpiresAt != nil && !sub.ExpiresAt.After(now) {
			continue
		}
		out = append(out, contracts.RealityUser{UUID: u.UUID, ShortID: u.RealityShortID})
	}
	return out, nil
}

func servable(s contracts.SubscriptionStatus) bool {
	switch s {
	case contracts.SubscriptionStatusActive, contracts.SubscriptionStatusTrialing, contracts.SubscriptionStatusPastDue:
		return true
	default:
		return false
	}
}

// NodeActivator is an in-memory NodeActivator combining the node + allow-list
// stores. It mirrors the Postgres guard set: provisioning, >=2 distinct enabled
// client transport types, paired exit active, and an unchanged allow-list
// revision. The nodes mutex serializes concurrent activations.
type NodeActivator struct {
	nodes *Nodes
	allow *AllowList
}

// NewNodeActivator wires the in-memory activator.
func NewNodeActivator(nodes *Nodes, allow *AllowList) *NodeActivator {
	return &NodeActivator{nodes: nodes, allow: allow}
}

func (a *NodeActivator) Activate(ctx context.Context, id, expectedRevision string, evidence contracts.NodeActivationEvidence) (contracts.Node, contracts.NodeStatus, error) {
	a.nodes.mu.Lock()
	defer a.nodes.mu.Unlock()
	n, ok := a.nodes.nodes[id]
	if !ok {
		return contracts.Node{}, "", domain.ErrNotFound
	}
	prev := n.Status
	if n.Status != contracts.NodeStatusProvisioning {
		return contracts.Node{}, prev, domain.ErrConflict
	}
	if n.Role == contracts.NodeRoleExit {
		// Exit: provisioning + paired entry + reconciler data-plane evidence.
		if n.EntryNodeID == nil || *n.EntryNodeID == "" {
			return contracts.Node{}, prev, domain.ErrConflict
		}
		if evidence != contracts.ExitDataPlaneVerified {
			return contracts.Node{}, prev, domain.ErrConflict
		}
	} else {
		if contracts.DistinctEnabledTransportTypes(n.Transports) < 2 {
			return contracts.Node{}, prev, domain.ErrConflict
		}
		exitActive := false
		for _, other := range a.nodes.nodes {
			if other.Role == contracts.NodeRoleExit && other.EntryNodeID != nil && *other.EntryNodeID == id {
				exitActive = other.Status == contracts.NodeStatusActive
				break
			}
		}
		if !exitActive {
			return contracts.Node{}, prev, domain.ErrConflict
		}
		users, err := a.allow.EligibleRealityUsers(ctx)
		if err != nil {
			return contracts.Node{}, prev, err
		}
		if users == nil {
			users = []contracts.RealityUser{}
		}
		if contracts.RealityUsersRevision(users) != expectedRevision {
			return contracts.Node{}, prev, domain.ErrConflict
		}
	}
	n.Status = contracts.NodeStatusActive
	a.nodes.nodes[id] = n
	return n, prev, nil
}

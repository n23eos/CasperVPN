package controlplane

import (
	"context"
	"sync"

	"github.com/caspervpn/contracts"
)

// Memory is an in-memory Provider + TokenIndex for dev and tests. It is the
// default when CONTROL_PLANE_URL is unset. Safe for concurrent use.
type Memory struct {
	mu     sync.RWMutex
	users  map[string]contracts.User
	subs   map[string]contracts.Subscription
	nodes  []contracts.Node
	tokens map[string]tokenRef
}

type tokenRef struct{ userID, subID string }

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		users:  map[string]contracts.User{},
		subs:   map[string]contracts.Subscription{},
		tokens: map[string]tokenRef{},
	}
}

// PutUser upserts a user (test/dev seeding).
func (m *Memory) PutUser(u contracts.User) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[u.ID] = u
}

// PutSubscription upserts a subscription (test/dev seeding).
func (m *Memory) PutSubscription(s contracts.Subscription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs[s.ID] = s
}

// SetNodes replaces the node set (test/dev seeding).
func (m *Memory) SetNodes(nodes []contracts.Node) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes = append(m.nodes[:0:0], nodes...)
}

// GetUser implements Provider.
func (m *Memory) GetUser(_ context.Context, id string) (contracts.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.users[id]
	if !ok {
		return contracts.User{}, ErrNotFound
	}
	return u, nil
}

// GetSubscription implements Provider.
func (m *Memory) GetSubscription(_ context.Context, id string) (contracts.Subscription, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.subs[id]
	if !ok {
		return contracts.Subscription{}, ErrNotFound
	}
	return s, nil
}

// ListNodes implements Provider, applying the filter over the seeded set.
func (m *Memory) ListNodes(_ context.Context, f NodeFilter) ([]contracts.Node, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]contracts.Node, 0, len(m.nodes))
	for _, n := range m.nodes {
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
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}

// Lookup implements TokenIndex.
func (m *Memory) Lookup(_ context.Context, token string) (string, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ref, ok := m.tokens[token]
	if !ok {
		return "", "", ErrNotFound
	}
	return ref.userID, ref.subID, nil
}

// Register implements TokenIndex.
func (m *Memory) Register(_ context.Context, token, userID, subID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[token] = tokenRef{userID: userID, subID: subID}
	return nil
}

// Revoke implements TokenIndex.
func (m *Memory) Revoke(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, token)
	return nil
}

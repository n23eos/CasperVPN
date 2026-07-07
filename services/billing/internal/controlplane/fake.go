package controlplane

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/caspervpn/billing/internal/idgen"
	"github.com/caspervpn/contracts"
)

// Fake is an in-memory Client for tests. It records call counts so tests can
// assert idempotency (e.g. two identical webhooks must not double SetPeriod calls
// beyond the single crediting path).
type Fake struct {
	mu    sync.Mutex
	users map[string]contracts.User
	subs  map[string]contracts.Subscription

	CreateCalls int
	SetCalls    int
}

// NewFake builds an empty fake control-plane.
func NewFake() *Fake {
	return &Fake{
		users: make(map[string]contracts.User),
		subs:  make(map[string]contracts.Subscription),
	}
}

// AddUser seeds an anonymous account.
func (f *Fake) AddUser(u contracts.User) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.ID] = u
}

// GetUser implements Client.
func (f *Fake) GetUser(_ context.Context, id string) (contracts.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return contracts.User{}, fmt.Errorf("fake: user %q not found", id)
	}
	return u, nil
}

// CreateSubscription implements Client.
func (f *Fake) CreateSubscription(_ context.Context, userID string, plan contracts.SubscriptionPlan) (contracts.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return contracts.Subscription{}, fmt.Errorf("fake: user %q not found", userID)
	}
	f.CreateCalls++
	now := time.Now()
	sub := contracts.Subscription{
		ID:        idgen.New(),
		UserID:    userID,
		Plan:      plan,
		Status:    contracts.SubscriptionStatusActive,
		Token:     idgen.New(),
		StartsAt:  now,
		CreatedAt: now,
		UpdatedAt: now,
	}
	f.subs[sub.ID] = sub
	u.SubscriptionID = &sub.ID
	f.users[userID] = u
	return sub, nil
}

// SetSubscriptionPeriod implements Client.
func (f *Fake) SetSubscriptionPeriod(_ context.Context, subID string, status contracts.SubscriptionStatus, expiresAt time.Time) (contracts.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sub, ok := f.subs[subID]
	if !ok {
		return contracts.Subscription{}, fmt.Errorf("fake: subscription %q not found", subID)
	}
	f.SetCalls++
	exp := expiresAt
	sub.Status = status
	sub.ExpiresAt = &exp
	sub.UpdatedAt = time.Now()
	f.subs[subID] = sub
	return sub, nil
}

// Subscription returns a stored subscription (test assertion helper).
func (f *Fake) Subscription(id string) (contracts.Subscription, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.subs[id]
	return s, ok
}

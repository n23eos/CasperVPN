package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/secret"
)

// Plan limits (0 = unlimited). Kept here, not hardcoded per-request; NO card or
// payment data is modeled — billing is a separate service (Agent E).
const (
	basicTrafficLimitBytes uint64 = 100 * 1024 * 1024 * 1024 // 100 GiB fair-use
	basicSpeedLimitMbps           = 50
	basicDeviceLimit              = 2
	unlimitedDeviceLimit          = 5
	tokenPrefixLen                = 8
)

// SubscriptionService owns entitlements. It never stores the subscription token
// in the clear — only its hash — and returns the plaintext once at creation.
type SubscriptionService struct {
	subs  domain.SubscriptionRepo
	users domain.UserRepo
	now   func() time.Time
}

// NewSubscriptionService wires a SubscriptionService.
func NewSubscriptionService(subs domain.SubscriptionRepo, users domain.UserRepo) *SubscriptionService {
	return &SubscriptionService{subs: subs, users: users, now: time.Now}
}

// Create issues a subscription for an existing user and returns the one-time
// plaintext token alongside the stored (hashed) record.
func (s *SubscriptionService) Create(ctx context.Context, userID string, plan contracts.SubscriptionPlan) (domain.SubscriptionWithToken, error) {
	if !plan.Valid() {
		return domain.SubscriptionWithToken{}, fmt.Errorf("%w: unknown plan %q", domain.ErrValidation, plan)
	}
	if _, err := s.users.Get(ctx, userID); err != nil {
		return domain.SubscriptionWithToken{}, err // ErrNotFound bubbles up
	}
	id, err := secret.UUIDv4()
	if err != nil {
		return domain.SubscriptionWithToken{}, err
	}
	token, err := secret.Token()
	if err != nil {
		return domain.SubscriptionWithToken{}, err
	}
	now := s.now()
	sub := contracts.Subscription{
		ID:        id,
		UserID:    userID,
		Plan:      plan,
		Status:    contracts.SubscriptionStatusActive,
		Token:     token, // returned to caller; NOT persisted in the clear
		StartsAt:  now,
		CreatedAt: now,
		UpdatedAt: now,
	}
	applyPlanLimits(&sub, plan)
	if err := sub.Validate(); err != nil {
		return domain.SubscriptionWithToken{}, fmt.Errorf("%w: %s", domain.ErrValidation, err)
	}
	if err := s.subs.Create(ctx, sub, secret.HashToken(token), secret.Prefix(token, tokenPrefixLen)); err != nil {
		return domain.SubscriptionWithToken{}, err
	}
	// Link the subscription onto the user.
	if u, err := s.users.Get(ctx, userID); err == nil {
		u.SubscriptionID = &sub.ID
		u.UpdatedAt = now
		_ = s.users.Update(ctx, u)
	}
	return domain.SubscriptionWithToken{Subscription: sub, PlainToken: token}, nil
}

// Get returns a subscription by id. The token is never exposed on read.
func (s *SubscriptionService) Get(ctx context.Context, id string) (contracts.Subscription, error) {
	sub, err := s.subs.Get(ctx, id)
	if err != nil {
		return contracts.Subscription{}, err
	}
	sub.Token = ""
	return sub, nil
}

func applyPlanLimits(sub *contracts.Subscription, plan contracts.SubscriptionPlan) {
	switch plan {
	case contracts.SubscriptionPlanBasic:
		sub.TrafficLimitBytes = basicTrafficLimitBytes
		sub.SpeedLimitMbps = basicSpeedLimitMbps
		sub.DeviceLimit = basicDeviceLimit
	case contracts.SubscriptionPlanUnlimited:
		sub.TrafficLimitBytes = 0
		sub.SpeedLimitMbps = 0
		sub.DeviceLimit = unlimitedDeviceLimit
	}
}

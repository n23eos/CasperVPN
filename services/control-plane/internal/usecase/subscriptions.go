package usecase

import (
	"context"
	"fmt"
	"log"
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
	subs    domain.SubscriptionRepo
	users   domain.UserRepo
	revoker domain.SubscriptionRevoker // optional; nil => no propagation
	now     func() time.Time
}

// NewSubscriptionService wires a SubscriptionService.
func NewSubscriptionService(subs domain.SubscriptionRepo, users domain.UserRepo) *SubscriptionService {
	return &SubscriptionService{subs: subs, users: users, now: time.Now}
}

// WithRevoker attaches the subscription-service notifier (TZ-token-revocation).
// Revocation/registration calls are best-effort: the local state change is
// authoritative and a notify failure is logged, not returned — the subscription
// service still converges via its cache TTL and status re-check on cache miss.
// Retries/circuit-breaking belong to TZ-hardening.
func (s *SubscriptionService) WithRevoker(r domain.SubscriptionRevoker) *SubscriptionService {
	s.revoker = r
	return s
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
	// Atomic create+link in ONE transaction with the user row locked (FOR UPDATE):
	// if the user already has a subscription the whole operation is rejected with
	// ErrConflict (HTTP 409) and NOTHING is written — the generated plaintext token
	// is simply discarded here, never persisted or returned. This closes both the
	// duplicate-create race (two callers each creating) and the orphan window (a
	// subscription can never exist unlinked). billing's renewal path relies on the
	// link holding, so it must be atomic with the insert.
	if err := s.subs.CreateAndLink(ctx, sub, secret.HashToken(token), secret.Prefix(token, tokenPrefixLen), userID); err != nil {
		return domain.SubscriptionWithToken{}, err
	}
	// Push the fresh token into the subscription service's index so /sub/{token}
	// resolves without waiting for an out-of-band sync (TZ-token-revocation §2).
	if s.revoker != nil {
		if err := s.revoker.RegisterToken(ctx, token, userID, sub.ID); err != nil {
			log.Printf("subscriptions: register token for sub %s: %v", sub.ID, err)
		}
	}
	return domain.SubscriptionWithToken{Subscription: sub, PlainToken: token}, nil
}

// Patch applies a true partial update (status/expires_at). Transitions into a
// non-servable status (canceled/expired) revoke the token downstream so the
// link dies immediately, not at cache TTL.
func (s *SubscriptionService) Patch(ctx context.Context, id string, patch contracts.SubscriptionPatch) (contracts.Subscription, error) {
	if err := patch.Validate(); err != nil {
		return contracts.Subscription{}, fmt.Errorf("%w: %s", domain.ErrValidation, err)
	}
	existing, err := s.subs.Get(ctx, id)
	if err != nil {
		return contracts.Subscription{}, err
	}
	if patch.IsZero() {
		existing.Token = ""
		return existing, nil
	}
	if patch.Status != nil {
		existing.Status = *patch.Status
	}
	if patch.ExpiresAt != nil {
		existing.ExpiresAt = patch.ExpiresAt
	}
	existing.UpdatedAt = s.now()
	if err := s.subs.Update(ctx, existing); err != nil {
		return contracts.Subscription{}, err
	}
	if patch.Status != nil && !servable(*patch.Status) {
		s.revokeSubscription(ctx, id)
	}
	existing.Token = ""
	return existing, nil
}

// Cancel sets status=canceled and revokes the token downstream.
func (s *SubscriptionService) Cancel(ctx context.Context, id string) (contracts.Subscription, error) {
	canceled := contracts.SubscriptionStatusCanceled
	return s.Patch(ctx, id, contracts.SubscriptionPatch{Status: &canceled})
}

// RotateToken issues a fresh token, stores only its hash, revokes the old one
// downstream and registers the new one. The plaintext rides in the returned
// Subscription.Token exactly once (leaked-link revocation without touching the
// account).
func (s *SubscriptionService) RotateToken(ctx context.Context, id string) (domain.SubscriptionWithToken, error) {
	existing, err := s.subs.Get(ctx, id)
	if err != nil {
		return domain.SubscriptionWithToken{}, err
	}
	token, err := secret.Token()
	if err != nil {
		return domain.SubscriptionWithToken{}, err
	}
	if err := s.subs.UpdateTokenHash(ctx, id, secret.HashToken(token), secret.Prefix(token, tokenPrefixLen)); err != nil {
		return domain.SubscriptionWithToken{}, err
	}
	existing.UpdatedAt = s.now()
	if err := s.subs.Update(ctx, existing); err != nil {
		return domain.SubscriptionWithToken{}, err
	}
	// Old link dies immediately; the new one starts resolving right away.
	s.revokeSubscription(ctx, id)
	if s.revoker != nil {
		if err := s.revoker.RegisterToken(ctx, token, existing.UserID, id); err != nil {
			log.Printf("subscriptions: register rotated token for sub %s: %v", id, err)
		}
	}
	existing.Token = token
	return domain.SubscriptionWithToken{Subscription: existing, PlainToken: token}, nil
}

// revokeSubscription best-effort pushes a revoke downstream (see WithRevoker).
func (s *SubscriptionService) revokeSubscription(ctx context.Context, id string) {
	if s.revoker == nil {
		return
	}
	if err := s.revoker.RevokeSubscription(ctx, id); err != nil {
		log.Printf("subscriptions: revoke sub %s downstream: %v", id, err)
	}
}

// servable mirrors the subscription service's resolve gate: these statuses
// still serve configs (past_due kept in grace).
func servable(st contracts.SubscriptionStatus) bool {
	switch st {
	case contracts.SubscriptionStatusActive, contracts.SubscriptionStatusTrialing, contracts.SubscriptionStatusPastDue:
		return true
	}
	return false
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

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

// defaultDeviceLimit is the anti-resale device cap for a fresh account.
const defaultDeviceLimit = 3

// UserService owns accounts and their PERSONAL isolation secrets. Every user
// gets a distinct reality_short_id + uuid + key, so burning one never exposes
// the others sharing a node.
type UserService struct {
	users     domain.UserRepo
	rotations domain.RotationRepo
	queue     domain.RebuildQueue
	revoker   domain.SubscriptionRevoker // optional; nil => no propagation
	now       func() time.Time
}

// NewUserService wires a UserService.
func NewUserService(users domain.UserRepo, rotations domain.RotationRepo, queue domain.RebuildQueue) *UserService {
	return &UserService{users: users, rotations: rotations, queue: queue, now: time.Now}
}

// WithRevoker attaches the subscription-service notifier (TZ-token-revocation).
// Best-effort: local state is authoritative; a notify failure is logged, and the
// subscription service still converges via cache TTL + status re-check on miss.
func (s *UserService) WithRevoker(r domain.SubscriptionRevoker) *UserService {
	s.revoker = r
	return s
}

// revokeUser best-effort kills every subscription token of a user downstream.
func (s *UserService) revokeUser(ctx context.Context, id, cause string) {
	if s.revoker == nil {
		return
	}
	if err := s.revoker.RevokeUser(ctx, id); err != nil {
		log.Printf("users: revoke tokens of user %s downstream (%s): %v", id, cause, err)
	}
}

// Create issues a new account with freshly generated personal secrets.
func (s *UserService) Create(ctx context.Context, telegramID *int64, email *string) (contracts.User, error) {
	id, err := secret.UUIDv4()
	if err != nil {
		return contracts.User{}, err
	}
	shortID, err := secret.RealityShortID()
	if err != nil {
		return contracts.User{}, err
	}
	uuid, err := secret.UUIDv4()
	if err != nil {
		return contracts.User{}, err
	}
	privKey, err := secret.PrivateKey()
	if err != nil {
		return contracts.User{}, err
	}
	now := s.now()
	u := contracts.User{
		ID:             id,
		TelegramID:     telegramID,
		Email:          email,
		Status:         contracts.UserStatusActive,
		RealityShortID: shortID,
		UUID:           uuid,
		PrivateKey:     privKey,
		DeviceLimit:    defaultDeviceLimit,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := u.Validate(); err != nil {
		return contracts.User{}, fmt.Errorf("%w: %s", domain.ErrValidation, err)
	}
	if err := s.users.Create(ctx, u); err != nil {
		return contracts.User{}, err
	}
	return u, nil
}

// Get returns a user by id.
func (s *UserService) Get(ctx context.Context, id string) (contracts.User, error) {
	return s.users.Get(ctx, id)
}

// Update changes mutable account fields (status, limits, contact). Personal
// secrets are immutable here — use RotateSecrets. id/secrets/created_at are
// preserved from the stored user.
func (s *UserService) Update(ctx context.Context, id string, patch contracts.User, actor string) (contracts.User, error) {
	existing, err := s.users.Get(ctx, id)
	if err != nil {
		return contracts.User{}, err
	}
	existing.Status = patch.Status
	existing.DeviceLimit = patch.DeviceLimit
	existing.QuotaBytes = patch.QuotaBytes
	existing.UsedBytes = patch.UsedBytes
	existing.TelegramID = patch.TelegramID
	existing.Email = patch.Email
	if patch.SubscriptionID != nil {
		existing.SubscriptionID = patch.SubscriptionID
	}
	existing.UpdatedAt = s.now()
	if err := existing.Validate(); err != nil {
		return contracts.User{}, fmt.Errorf("%w: %s", domain.ErrValidation, err)
	}
	if err := s.users.Update(ctx, existing); err != nil {
		return contracts.User{}, err
	}
	// Leaving the active state (ban/suspend/expire) must kill the user's
	// subscription links immediately, not at cache TTL (TZ-token-revocation §2).
	if existing.Status != contracts.UserStatusActive {
		s.revokeUser(ctx, id, "status "+string(existing.Status))
	}
	return existing, nil
}

// RotateSecrets replaces a user's personal reality_short_id/uuid/key (abuse
// response). The old values are archived, and the user's set is rebuilt so the
// new secrets propagate to their subscription output.
func (s *UserService) RotateSecrets(ctx context.Context, id, reason, actor string) (contracts.User, error) {
	existing, err := s.users.Get(ctx, id)
	if err != nil {
		return contracts.User{}, err
	}
	shortID, err := secret.RealityShortID()
	if err != nil {
		return contracts.User{}, err
	}
	uuid, err := secret.UUIDv4()
	if err != nil {
		return contracts.User{}, err
	}
	privKey, err := secret.PrivateKey()
	if err != nil {
		return contracts.User{}, err
	}
	updated, err := s.users.RotateSecrets(ctx, id, shortID, uuid, privKey)
	if err != nil {
		return contracts.User{}, err
	}
	histID, err := secret.UUIDv4()
	if err != nil {
		return contracts.User{}, err
	}
	if reason == "" {
		reason = "rotate-secrets"
	}
	rec := domain.UserSecretRotation{
		ID: histID, UserID: id,
		OldRealityShortID: existing.RealityShortID,
		OldVlessUUID:      existing.UUID,
		Reason:            reason,
		CreatedAt:         s.now(),
	}
	if err := s.rotations.AddUserSecretRotation(ctx, rec); err != nil {
		return contracts.User{}, err
	}
	// Abuse response: the old link must stop resolving now. The user gets a new
	// link via subscription re-issue/rotation (TZ-token-revocation §2).
	s.revokeUser(ctx, id, "rotate-secrets")
	s.queue.EnqueueUser(id)
	return updated, nil
}

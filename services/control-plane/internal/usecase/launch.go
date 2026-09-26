package usecase

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/secret"
)

// WithTokenCipher configures the external key used for recoverable delivery aliases.
func (s *SubscriptionService) WithTokenCipher(c *secret.TokenCipher) *SubscriptionService {
	s.cipher = c
	return s
}

// Ensure creates one unpaid subscription or returns the existing subscription.
func (s *SubscriptionService) Ensure(ctx context.Context, userID string, plan contracts.SubscriptionPlan) (contracts.Subscription, error) {
	if !plan.Valid() {
		return contracts.Subscription{}, domain.ErrValidation
	}
	user, err := s.users.Get(ctx, userID)
	if err != nil {
		return contracts.Subscription{}, err
	}
	if user.SubscriptionID != nil {
		return s.Get(ctx, *user.SubscriptionID)
	}
	result, err := s.create(ctx, userID, plan, true)
	if errors.Is(err, domain.ErrConflict) {
		user, err = s.users.Get(ctx, userID)
		if err != nil {
			return contracts.Subscription{}, err
		}
		if user.SubscriptionID != nil {
			return s.Get(ctx, *user.SubscriptionID)
		}
	}
	result.Subscription.Token = ""
	return result.Subscription, err
}

// ApplyBillingState validates and applies a durable billing snapshot atomically.
func (s *SubscriptionService) ApplyBillingState(ctx context.Context, id string, state contracts.BillingState) (contracts.Subscription, error) {
	if err := state.Validate(); err != nil {
		return contracts.Subscription{}, fmt.Errorf("%w: %s", domain.ErrValidation, err)
	}
	state.ExpiresAt = state.ExpiresAt.UTC().Truncate(time.Microsecond)
	state.GraceUntil = state.GraceUntil.UTC().Truncate(time.Microsecond)
	repo, ok := s.subs.(domain.BillingStateRepo)
	if !ok {
		return contracts.Subscription{}, fmt.Errorf("billing state repository unavailable")
	}
	return repo.ApplyBillingState(ctx, id, state)
}

// DeliveryLink returns the same encrypted-at-rest alias across retries and restarts.
func (s *SubscriptionService) DeliveryLink(ctx context.Context, id string) (contracts.DeliveryLink, error) {
	repo, ok := s.subs.(domain.DeliveryTokenRepo)
	if !ok || s.cipher == nil {
		return contracts.DeliveryLink{}, fmt.Errorf("delivery token service unavailable")
	}
	sub, err := s.Get(ctx, id)
	if err != nil {
		return contracts.DeliveryLink{}, err
	}
	user, err := s.users.Get(ctx, sub.UserID)
	if err != nil {
		return contracts.DeliveryLink{}, err
	}
	if user.Status != contracts.UserStatusActive || !servable(sub.Status) || (sub.AccessUntil() != nil && !sub.AccessUntil().After(s.now())) {
		return contracts.DeliveryLink{}, domain.ErrConflict
	}
	token, err := secret.Token()
	if err != nil {
		return contracts.DeliveryLink{}, err
	}
	encrypted, err := s.cipher.Seal(id, token)
	if err != nil {
		return contracts.DeliveryLink{}, err
	}
	saved, err := repo.EnsureDeliveryToken(ctx, id, domain.DeliveryToken{Hash: secret.HashToken(token), Ciphertext: encrypted, KeyID: s.cipher.ID})
	if err != nil {
		return contracts.DeliveryLink{}, err
	}
	token, err = s.cipher.Open(id, saved.KeyID, saved.Ciphertext)
	if err != nil {
		return contracts.DeliveryLink{}, err
	}
	return contracts.DeliveryLink{Token: token, SubscriptionID: id}, nil
}

// ResolveToken checks a digest against the authoritative primary and alias stores.
func (s *SubscriptionService) ResolveToken(ctx context.Context, hash string) (contracts.SubscriptionTokenBinding, error) {
	b, err := hex.DecodeString(hash)
	if err != nil || len(b) != 32 {
		return contracts.SubscriptionTokenBinding{}, domain.ErrValidation
	}
	repo, ok := s.subs.(domain.DeliveryTokenRepo)
	if !ok {
		return contracts.SubscriptionTokenBinding{}, fmt.Errorf("token authority unavailable")
	}
	return repo.ResolveTokenHash(ctx, hash)
}

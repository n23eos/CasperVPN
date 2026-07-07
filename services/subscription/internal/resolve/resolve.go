// Package resolve turns an opaque subscription token into a canonical
// SubscriptionBundle: it looks the token up, validates the subscription and
// account state, and assembles the user + active nodes. Validation failures are
// typed so the HTTP layer can map them to 401/404/410.
package resolve

import (
	"context"
	"errors"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/subscription/internal/controlplane"
)

// Typed resolution errors (HTTP codes per the subscription OpenAPI).
var (
	// ErrUnknownToken => 401 (unknown or revoked token, or banned/suspended user).
	ErrUnknownToken = errors.New("resolve: unknown or revoked token")
	// ErrNoSubscription => 404 (no subscription behind the token).
	ErrNoSubscription = errors.New("resolve: no subscription for token")
	// ErrExpired => 410 (subscription lapsed).
	ErrExpired = errors.New("resolve: subscription expired")
)

// Resolver assembles bundles from the token index and control-plane provider.
type Resolver struct {
	idx controlplane.TokenIndex
	cp  controlplane.Provider
	now func() time.Time
}

// New builds a Resolver. now may be nil (defaults to time.Now).
func New(idx controlplane.TokenIndex, cp controlplane.Provider, now func() time.Time) *Resolver {
	if now == nil {
		now = time.Now
	}
	return &Resolver{idx: idx, cp: cp, now: now}
}

// Resolved carries the assembled bundle plus the subscription (needed for plan
// filtering and client traffic/expiry headers).
type Resolved struct {
	Bundle       contracts.SubscriptionBundle
	Subscription contracts.Subscription
}

// Resolve looks up token, validates state, and returns the raw (unfiltered)
// bundle of the user + currently-active nodes.
func (r *Resolver) Resolve(ctx context.Context, token string) (Resolved, error) {
	userID, subID, err := r.idx.Lookup(ctx, token)
	if err != nil {
		return Resolved{}, ErrUnknownToken
	}

	sub, err := r.cp.GetSubscription(ctx, subID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return Resolved{}, ErrNoSubscription
		}
		return Resolved{}, err
	}
	// The token is already authenticated by idx.Lookup above (the token index is
	// the source of truth for token->sub binding). We intentionally do NOT compare
	// against sub.Token: control-plane never returns the plaintext token on read
	// (GET /v1/subscriptions/{id} zeroes it), so such a check would reject every
	// real resolve — and returning the plaintext token just to compare would leak it.
	if err := r.checkSubscription(sub); err != nil {
		return Resolved{}, err
	}

	user, err := r.cp.GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return Resolved{}, ErrNoSubscription
		}
		return Resolved{}, err
	}
	if user.Status != contracts.UserStatusActive {
		// Suspended / banned / expired accounts do not get a config.
		return Resolved{}, ErrUnknownToken
	}

	nodes, err := r.cp.ListNodes(ctx, controlplane.NodeFilter{Status: contracts.NodeStatusActive})
	if err != nil {
		return Resolved{}, err
	}

	return Resolved{
		Bundle: contracts.SubscriptionBundle{
			User:        user,
			Nodes:       nodes,
			GeneratedAt: r.now().UTC(),
		},
		Subscription: sub,
	}, nil
}

// checkSubscription rejects lapsed subscriptions with ErrExpired.
func (r *Resolver) checkSubscription(s contracts.Subscription) error {
	switch s.Status {
	case contracts.SubscriptionStatusActive, contracts.SubscriptionStatusTrialing, contracts.SubscriptionStatusPastDue:
		// servable (past_due kept in grace so a payment blip does not cut access)
	default: // canceled, expired
		return ErrExpired
	}
	if s.ExpiresAt != nil && !s.ExpiresAt.After(r.now()) {
		return ErrExpired
	}
	return nil
}

// Package controlplane is billing's dependency on the authoritative control-plane
// service, expressed as a narrow interface billing owns. This keeps billing
// physically decoupled: it talks to control-plane only over HTTP, and tests run
// against an in-memory fake.
package controlplane

import (
	"context"
	"time"

	"github.com/caspervpn/contracts"
)

// Client is what billing needs from control-plane. It is intentionally minimal.
//
// NOTE: SetSubscriptionPeriod requires an ADDITIVE control-plane endpoint that the
// frozen OpenAPI does not yet define — see docs/billing.md ("Контрактный пробел").
// The frozen contract offers create + get only; renewal/expiry needs a
// backward-compatible PATCH /v1/subscriptions/{id}. This interface is the seam.
type Client interface {
	// GetUser fetches the anonymous account (its per-user isolation secrets and its
	// current subscription id, if any).
	GetUser(ctx context.Context, id string) (contracts.User, error)

	// CreateSubscription creates a subscription for a user and returns it (including
	// the issued sub_token). Maps to POST /v1/subscriptions.
	CreateSubscription(ctx context.Context, userID string, plan contracts.SubscriptionPlan) (contracts.Subscription, error)

	// SetSubscriptionPeriod sets the status and expiry of an existing subscription —
	// the activate/renew/expire operation. Maps to the additive
	// PATCH /v1/subscriptions/{id}.
	SetSubscriptionPeriod(ctx context.Context, subID string, status contracts.SubscriptionStatus, expiresAt time.Time) (contracts.Subscription, error)
}

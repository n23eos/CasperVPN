// Package controlplane is the subscription service's outbound port to the
// control-plane (node registry, users, subscriptions) plus the subscription-
// owned token index. The service depends on these interfaces, never on another
// team's package — adapters (HTTP, in-memory) satisfy them.
package controlplane

import (
	"context"
	"errors"

	"github.com/caspervpn/contracts"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("controlplane: not found")

// NodeFilter narrows a ListNodes query. Zero-value fields are unconstrained.
type NodeFilter struct {
	Status contracts.NodeStatus // e.g. active
	Role   contracts.NodeRole
	Region string
	Limit  int
}

// Provider reads authoritative state from the control-plane. It maps onto the
// frozen control-plane OpenAPI: GET /v1/users/{id}, GET /v1/subscriptions/{id},
// GET /v1/nodes.
type Provider interface {
	GetUser(ctx context.Context, id string) (contracts.User, error)
	GetSubscription(ctx context.Context, id string) (contracts.Subscription, error)
	ListNodes(ctx context.Context, f NodeFilter) ([]contracts.Node, error)
}

// TokenIndex resolves the opaque subscription token embedded in a /sub/{token}
// URL to the user and subscription it belongs to. Owning this mapping is the
// subscription service's bounded context (per-user subscription URL); it is fed
// out-of-band by billing/control-plane via Register.
type TokenIndex interface {
	// Lookup returns the userID and subscriptionID for a token, or ErrNotFound.
	Lookup(ctx context.Context, token string) (userID, subID string, err error)
	// Register upserts a token -> (userID, subID) mapping.
	Register(ctx context.Context, token, userID, subID string) error
	// Revoke removes a token mapping (leaked-link revocation).
	Revoke(ctx context.Context, token string) error
}

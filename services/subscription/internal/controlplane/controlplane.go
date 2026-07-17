// Package controlplane is the subscription service's outbound port to the
// control-plane (node registry, users, subscriptions) plus the subscription-
// owned token index. The service depends on these interfaces, never on another
// team's package — adapters (HTTP, in-memory) satisfy them.
package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
//
// Implementations MUST store tokens hashed (SHA-256, hex — same as the
// control-plane's secret.HashToken), never in plaintext: the index is exactly
// the kind of at-rest data a seized/leaked instance would expose
// (TZ-token-revocation §3). Plaintext exists only in flight (URL, Register).
type TokenIndex interface {
	// Lookup returns the userID and subscriptionID for a token, or ErrNotFound.
	Lookup(ctx context.Context, token string) (userID, subID string, err error)
	// Register upserts a token -> (userID, subID) mapping.
	Register(ctx context.Context, token, userID, subID string) error
	// Revoke removes a token mapping (leaked-link revocation).
	Revoke(ctx context.Context, token string) error
	// RevokeByUser removes every mapping of a user (ban/suspend). Returns how
	// many mappings were removed.
	RevokeByUser(ctx context.Context, userID string) (int, error)
	// RevokeBySubscription removes the mapping(s) of one subscription
	// (cancel/expire/rotate). Returns how many mappings were removed.
	RevokeBySubscription(ctx context.Context, subID string) (int, error)
}

// HashToken is the canonical at-rest form of a subscription token in the index:
// hex SHA-256, matching the control-plane's secret.HashToken. Tokens are high
// entropy, so a fast unsalted hash is sufficient.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

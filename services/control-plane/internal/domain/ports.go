package domain

import (
	"context"

	"github.com/caspervpn/contracts"
)

// NodeRepo persists the dynamic node registry.
type NodeRepo interface {
	Create(ctx context.Context, n contracts.Node) error
	Get(ctx context.Context, id string) (contracts.Node, error)
	List(ctx context.Context, f NodeFilter) (items []contracts.Node, nextCursor string, err error)
	Update(ctx context.Context, n contracts.Node) error
	// SetStatus transitions status atomically and returns the previous status.
	SetStatus(ctx context.Context, id string, status contracts.NodeStatus) (prev contracts.NodeStatus, err error)
	// Demote atomically sets status=provisioning ONLY from a non-terminal state
	// (active/degraded/blocked/provisioning). draining/retired -> ErrConflict, so a
	// stale reconciler cannot resurrect an operator-retired/draining node.
	Demote(ctx context.Context, id string) (prev contracts.NodeStatus, err error)
	// SetEntryIP updates the advertised ingress address.
	SetEntryIP(ctx context.Context, id, entryIP string) error
	// ListActive returns nodes serving traffic (status = active), with transports.
	ListActive(ctx context.Context) ([]contracts.Node, error)
}

// UserRepo persists accounts and their personal isolation secrets.
type UserRepo interface {
	Create(ctx context.Context, u contracts.User) error
	Get(ctx context.Context, id string) (contracts.User, error)
	Update(ctx context.Context, u contracts.User) error
	// RotateSecrets swaps the personal secrets atomically.
	RotateSecrets(ctx context.Context, id, shortID, uuid, privKey string) (contracts.User, error)
	// AllActiveIDs lists ids of users whose sets should be (re)built.
	AllActiveIDs(ctx context.Context) ([]string, error)
}

// AllowListRepo answers the node REALITY allow-list: the (uuid, short_id) of
// every user whose account is active AND whose subscription is servable and not
// expired — matching the subscription service's eligibility exactly.
type AllowListRepo interface {
	EligibleRealityUsers(ctx context.Context) ([]contracts.RealityUser, error)
}

// NodeActivator atomically promotes a provisioning node to active, gating on
// structure (>=2 distinct enabled client transport types, paired exit active) and
// an unchanged allow-list revision. Returns the activated node and its previous
// status. ErrNotFound if absent; ErrConflict for any failed guard or a concurrent
// activation.
type NodeActivator interface {
	Activate(ctx context.Context, id, expectedRevision string, evidence contracts.NodeActivationEvidence) (contracts.Node, contracts.NodeStatus, error)
}

// SubscriptionRepo persists subscriptions. NO card/payment data — hashed token only.
type SubscriptionRepo interface {
	Create(ctx context.Context, s contracts.Subscription, tokenHash, tokenPrefix string) error
	// CreateAndLink inserts the subscription AND links it onto the user in ONE atomic
	// step: the user row is locked (SELECT ... FOR UPDATE), and if it already has a
	// subscription the whole operation is rejected with ErrConflict (nothing written).
	// This closes both the duplicate-create race (two callers can't each create) and
	// the orphan window (a subscription can never exist unlinked). ErrNotFound if the
	// user is absent.
	CreateAndLink(ctx context.Context, s contracts.Subscription, tokenHash, tokenPrefix, userID string) error
	Get(ctx context.Context, id string) (contracts.Subscription, error)
	// Update persists mutable entitlement fields (status, expires_at, updated_at).
	// The token hash is NOT touched here — see UpdateTokenHash.
	Update(ctx context.Context, s contracts.Subscription) error
	// UpdateTokenHash swaps the stored token hash/prefix (token rotation). The
	// plaintext token never reaches the repository.
	UpdateTokenHash(ctx context.Context, id, tokenHash, tokenPrefix string) error
}

// RotationRepo persists node and user-secret rotation history.
type RotationRepo interface {
	AddNodeRotation(ctx context.Context, r RotationRecord) error
	ListNodeRotations(ctx context.Context, nodeID string, limit int) ([]RotationRecord, error)
	AddUserSecretRotation(ctx context.Context, r UserSecretRotation) error
}

// SetRepo persists built per-user subscription sets (cache + source for Agent C).
type SetRepo interface {
	Get(ctx context.Context, userID string) (SubscriptionSet, error)
	Upsert(ctx context.Context, s SubscriptionSet) error
	// UserIDsByNode returns users whose current set includes the given node.
	UserIDsByNode(ctx context.Context, nodeID string) ([]string, error)
	// MarkDirtyByNode flags sets containing the node as stale (cache invalidation).
	MarkDirtyByNode(ctx context.Context, nodeID string) error
}

// SignalRepo persists FieldSignal aggregates.
type SignalRepo interface {
	AddAggregate(ctx context.Context, a SignalAggregate, blockSignals int, verdict Verdict) error
}

// SubscriptionRevoker is the outbound port to the subscription service's
// internal API (TZ-token-revocation). Ban/cancel/rotate must immediately kill
// the token in the subscription service's index AND flush its render cache —
// otherwise a revoked link keeps being served until the cache TTL expires.
// Implementations are best-effort at this layer (resilience hardening — retries,
// circuit breaker — is TZ-hardening); a no-op implementation is used in dev when
// SUBSCRIPTION_INTERNAL_URL is unset.
type SubscriptionRevoker interface {
	// RevokeUser revokes every token belonging to a user (ban/suspend).
	RevokeUser(ctx context.Context, userID string) error
	// RevokeSubscription revokes the token(s) of one subscription
	// (cancel/expire/rotate) without touching the user's other state.
	RevokeSubscription(ctx context.Context, subscriptionID string) error
	// RegisterToken registers a fresh plaintext token -> (user, subscription)
	// mapping (creation/rotation). The subscription service stores only a hash.
	RegisterToken(ctx context.Context, token, userID, subscriptionID string) error
}

// RebuildQueue asynchronously rebuilds affected users' active sets when the
// fleet changes (e.g. a node is blocked) and invalidates their cache.
type RebuildQueue interface {
	// EnqueueNode schedules a rebuild for every user affected by this node.
	EnqueueNode(nodeID string)
	// EnqueueUser schedules a rebuild for a single user.
	EnqueueUser(userID string)
}

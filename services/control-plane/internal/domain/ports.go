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

// SubscriptionRepo persists subscriptions. NO card/payment data — hashed token only.
type SubscriptionRepo interface {
	Create(ctx context.Context, s contracts.Subscription, tokenHash, tokenPrefix string) error
	Get(ctx context.Context, id string) (contracts.Subscription, error)
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

// RebuildQueue asynchronously rebuilds affected users' active sets when the
// fleet changes (e.g. a node is blocked) and invalidates their cache.
type RebuildQueue interface {
	// EnqueueNode schedules a rebuild for every user affected by this node.
	EnqueueNode(nodeID string)
	// EnqueueUser schedules a rebuild for a single user.
	EnqueueUser(userID string)
}

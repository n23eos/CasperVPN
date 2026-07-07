package domain

import (
	"time"

	"github.com/caspervpn/contracts"
)

// NodeFilter narrows a node listing. Empty fields mean "no filter".
type NodeFilter struct {
	Status contracts.NodeStatus
	Role   contracts.NodeRole
	Region string
	Limit  int
	Cursor string // keyset cursor: last node id from the previous page
}

// RotationRecord is one row of node lifecycle history (register/rotate/retire
// and status transitions). It is the audit trail of the fleet.
type RotationRecord struct {
	ID         string
	NodeID     string
	FromStatus *contracts.NodeStatus
	ToStatus   contracts.NodeStatus
	Reason     string
	EntryIP    string
	Actor      string
	CreatedAt  time.Time
}

// UserSecretRotation records a rotation of a user's personal isolation secrets.
type UserSecretRotation struct {
	ID                string
	UserID            string
	OldRealityShortID string
	OldVlessUUID      string
	Reason            string
	CreatedAt         time.Time
}

// SubscriptionSet is the built per-user Node×Transport set: the structured data
// handed to the subscription service (Agent C), plus cache/invalidation state.
type SubscriptionSet struct {
	UserID      string
	Revision    int64
	NodeIDs     []string
	Bundle      contracts.SubscriptionBundle
	Dirty       bool
	GeneratedAt time.Time
}

// SignalAggregate is one telemetry-side aggregate of FieldSignals for a node in
// a coarse region over a time window. It carries no user identity.
type SignalAggregate struct {
	NodeID      string
	Region      string
	WindowStart time.Time
	WindowEnd   time.Time
	Total       int
	BySignal    map[contracts.SignalType]int
	LossPctAvg  float64
	RTTmsAvg    int
}

// Verdict is control-plane's derived health verdict for a node from an aggregate.
type Verdict string

const (
	VerdictHealthy  Verdict = "healthy"
	VerdictDegraded Verdict = "degraded"
	VerdictBlocked  Verdict = "blocked"
)

// NodeStatusChange reports a node whose status the signal ingest moved.
type NodeStatusChange struct {
	NodeID string               `json:"node_id"`
	From   contracts.NodeStatus `json:"from"`
	To     contracts.NodeStatus `json:"to"`
}

// SubscriptionWithToken carries a freshly created subscription together with its
// one-time plaintext token (never persisted in the clear).
type SubscriptionWithToken struct {
	Subscription contracts.Subscription
	PlainToken   string
}

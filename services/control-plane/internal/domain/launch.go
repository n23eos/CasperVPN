package domain

import (
	"context"

	"github.com/caspervpn/contracts"
)

// Optional repository capabilities keep the existing storage seams compatible.
// TelegramUserRepo locates an existing sender account after a concurrent creation.
type TelegramUserRepo interface {
	GetByTelegram(context.Context, int64) (contracts.User, error)
}

// BillingStateRepo applies absolute entitlement snapshots under a revision fence.
type BillingStateRepo interface {
	ApplyBillingState(context.Context, string, contracts.BillingState) (contracts.Subscription, error)
}

// DeliveryToken is an encrypted recoverable alias and its authentication digest.
type DeliveryToken struct {
	Hash       string
	Ciphertext []byte
	KeyID      string
}

// DeliveryTokenRepo atomically persists aliases and resolves both token families.
type DeliveryTokenRepo interface {
	EnsureDeliveryToken(context.Context, string, DeliveryToken) (DeliveryToken, error)
	ResolveTokenHash(context.Context, string) (contracts.SubscriptionTokenBinding, error)
}

// AccessListRepo returns the complete eligible multi-transport admission snapshot.
type AccessListRepo interface {
	EligibleAccessUsers(context.Context) (contracts.NodeAccessUsers, error)
}

// AccessNodeActivator verifies the combined credential revision before activation.
type AccessNodeActivator interface {
	ActivateAccess(context.Context, string, string, contracts.NodeActivationEvidence) (contracts.Node, contracts.NodeStatus, error)
}

// SubscriptionCanceler cancels effective access without changing the billing ledger revision.
type SubscriptionCanceler interface {
	CancelSubscription(context.Context, string) (contracts.Subscription, error)
}

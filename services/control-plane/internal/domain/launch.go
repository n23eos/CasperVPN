package domain

import (
	"context"
	"github.com/caspervpn/contracts"
)

// Optional repository capabilities keep the existing storage seams compatible.
type TelegramUserRepo interface {
	GetByTelegram(context.Context, int64) (contracts.User, error)
}
type BillingStateRepo interface {
	ApplyBillingState(context.Context, string, contracts.BillingState) (contracts.Subscription, error)
}
type DeliveryToken struct {
	Hash       string
	Ciphertext []byte
	KeyID      string
}
type DeliveryTokenRepo interface {
	EnsureDeliveryToken(context.Context, string, DeliveryToken) (DeliveryToken, error)
	ResolveTokenHash(context.Context, string) (contracts.SubscriptionTokenBinding, error)
}
type AccessListRepo interface {
	EligibleAccessUsers(context.Context) (contracts.NodeAccessUsers, error)
}
type AccessNodeActivator interface {
	ActivateAccess(context.Context, string, string, contracts.NodeActivationEvidence) (contracts.Node, contracts.NodeStatus, error)
}

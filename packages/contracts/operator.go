package contracts

import "time"

// OperatorAccount is a safe projection for the private operator overview.
// It deliberately contains no contact information, tokens or access secrets.
type OperatorAccount struct {
	ID                 string             `json:"id"`
	Status             UserStatus         `json:"status"`
	SubscriptionID     *string            `json:"subscription_id,omitempty"`
	Plan               SubscriptionPlan   `json:"plan,omitempty"`
	SubscriptionStatus SubscriptionStatus `json:"subscription_status,omitempty"`
	ExpiresAt          *time.Time         `json:"expires_at,omitempty"`
	GraceUntil         *time.Time         `json:"grace_until,omitempty"`
	UpdatedAt          time.Time          `json:"updated_at"`
}

type ControlPlaneOperatorSummary struct {
	Users         map[UserStatus]int64         `json:"users"`
	Subscriptions map[SubscriptionStatus]int64 `json:"subscriptions"`
	Recent        []OperatorAccount            `json:"recent"`
}

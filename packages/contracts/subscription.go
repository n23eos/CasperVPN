package contracts

import (
	"fmt"
	"time"
)

// Subscription is the billing entitlement behind a User. It gates the
// subscription output (node list) the delivery/subscription services hand out.
type Subscription struct {
	ID     string             `json:"id"`
	UserID string             `json:"user_id"`
	Plan   SubscriptionPlan   `json:"plan"`
	Status SubscriptionStatus `json:"status"`

	// Token is the opaque secret embedded in the subscription URL. Rotatable so a
	// leaked link can be revoked without touching the account.
	Token string `json:"token"`

	// Limits (0 = unlimited). Fair-use throttle drives off TrafficLimitBytes.
	TrafficLimitBytes uint64 `json:"traffic_limit_bytes"`
	SpeedLimitMbps    int    `json:"speed_limit_mbps"`
	DeviceLimit       int    `json:"device_limit"`

	StartsAt  time.Time  `json:"starts_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // nil = open-ended
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Validate performs shallow structural checks on a Subscription.
func (s Subscription) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("contracts: subscription id is required")
	}
	if s.UserID == "" {
		return fmt.Errorf("contracts: subscription %q requires user_id", s.ID)
	}
	if !s.Plan.Valid() {
		return fmt.Errorf("contracts: unknown subscription plan %q", s.Plan)
	}
	if !s.Status.Valid() {
		return fmt.Errorf("contracts: unknown subscription status %q", s.Status)
	}
	if s.Token == "" {
		return fmt.Errorf("contracts: subscription %q requires a token", s.ID)
	}
	return nil
}

package contracts

import (
	"fmt"
	"time"
)

// User is an account. Its anti-block DNA is per-user isolation: each user carries
// a PERSONAL RealityShortID and a PERSONAL UUID/key. Nodes are shared for
// economics, but because every user is cryptographically distinct, blocking or
// burning one user never exposes the others on the same node.
type User struct {
	ID string `json:"id"`

	// Contact / identity. Telegram is the primary mass-market channel; email is
	// optional. Both nullable — no PII is required to exist.
	TelegramID *int64  `json:"telegram_id,omitempty"`
	Email      *string `json:"email,omitempty"`

	Status UserStatus `json:"status"`

	// Per-user isolation secrets. These are unique per user and rotate on abuse.
	RealityShortID string `json:"reality_short_id"`      // user's own REALITY short-id
	UUID           string `json:"uuid"`                  // user's own VLESS UUID
	PrivateKey     string `json:"private_key,omitempty"` // user's own transport key material (server-side secret)

	// Entitlements.
	SubscriptionID *string `json:"subscription_id,omitempty"`
	DeviceLimit    int     `json:"device_limit"` // anti multi-account resale

	// Traffic accounting for fair-use throttle (bytes).
	QuotaBytes uint64 `json:"quota_bytes"` // 0 = unlimited
	UsedBytes  uint64 `json:"used_bytes"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate performs shallow structural checks on a User.
func (u User) Validate() error {
	if u.ID == "" {
		return fmt.Errorf("contracts: user id is required")
	}
	if !u.Status.Valid() {
		return fmt.Errorf("contracts: unknown user status %q", u.Status)
	}
	if u.RealityShortID == "" {
		return fmt.Errorf("contracts: user %q requires a personal reality_short_id", u.ID)
	}
	if u.UUID == "" {
		return fmt.Errorf("contracts: user %q requires a personal uuid", u.ID)
	}
	if u.DeviceLimit < 0 {
		return fmt.Errorf("contracts: user %q device_limit must be >= 0", u.ID)
	}
	return nil
}

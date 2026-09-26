package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// BillingState is an absolute entitlement snapshot from billing's durable ledger.
type BillingState struct {
	Plan       SubscriptionPlan   `json:"plan,omitempty"`
	Revision   int64              `json:"revision"`
	Status     SubscriptionStatus `json:"status"`
	ExpiresAt  time.Time          `json:"expires_at"`
	GraceUntil time.Time          `json:"grace_until"`
}

func (s BillingState) Validate() error {
	if (s.Plan != "" && !s.Plan.Valid()) || s.Revision <= 0 || !s.Status.Valid() || s.ExpiresAt.IsZero() || s.GraceUntil.Before(s.ExpiresAt) {
		return fmt.Errorf("contracts: valid revision, status, expiry and grace are required")
	}
	return nil
}

// EnsureTelegram identifies the verified sender in a private Telegram update.
type EnsureTelegram struct {
	TelegramID int64 `json:"telegram_id"`
}

// EnsureSubscription selects the plan for initial unpaid subscription creation.
type EnsureSubscription struct {
	Plan SubscriptionPlan `json:"plan"`
}

// DeliveryLink returns the recoverable subscription alias to the delivery service.
type DeliveryLink struct {
	Token          string `json:"token"`
	SubscriptionID string `json:"subscription_id"`
}

// ResolveSubscriptionToken looks up a SHA-256 token digest without sending plaintext.
type ResolveSubscriptionToken struct {
	TokenHash string `json:"token_hash"`
}

// SubscriptionTokenBinding is the authoritative account and subscription for a token.
type SubscriptionTokenBinding struct {
	UserID         string `json:"user_id"`
	SubscriptionID string `json:"subscription_id"`
}

// AccessUser contains the personal credentials admitted on an entry node.
type AccessUser struct {
	UUID              string `json:"uuid"`
	ShortID           string `json:"short_id"`
	Hysteria2Password string `json:"hysteria2_password"`
}

// NodeAccessUsers is a complete admission snapshot with a bounded lease.
type NodeAccessUsers struct {
	Revision   string       `json:"revision"`
	ValidUntil time.Time    `json:"valid_until"`
	Users      []AccessUser `json:"users"`
}

// AccessUsersRevision excludes lease expiry: refreshing an unchanged set keeps its revision.
func AccessUsersRevision(users []AccessUser) string {
	s := append([]AccessUser{}, users...)
	sort.Slice(s, func(i, j int) bool {
		if s[i].UUID != s[j].UUID {
			return s[i].UUID < s[j].UUID
		}
		if s[i].ShortID != s[j].ShortID {
			return s[i].ShortID < s[j].ShortID
		}
		return s[i].Hysteria2Password < s[j].Hysteria2Password
	})
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// AccessUntil uses grace consistently in admission and subscription resolution.
func (s Subscription) AccessUntil() *time.Time {
	if s.GraceUntil != nil {
		return s.GraceUntil
	}
	return s.ExpiresAt
}

package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/caspervpn/contracts"
)

// BillingFingerprint records the billing intent independently of later manual
// cancellation. Replaying an already applied credit must not undo cancellation
// or fail because an operator changed the effective status afterward.
func BillingFingerprint(state contracts.BillingState, currentPlan contracts.SubscriptionPlan) string {
	if state.Plan == "" {
		state.Plan = currentPlan
	}
	state.ExpiresAt = state.ExpiresAt.UTC().Truncate(time.Microsecond)
	state.GraceUntil = state.GraceUntil.UTC().Truncate(time.Microsecond)
	b, _ := json.Marshal(state)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

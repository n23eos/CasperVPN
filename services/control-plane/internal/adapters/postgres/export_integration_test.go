//go:build integration

package postgres

import (
	"context"

	"github.com/caspervpn/contracts"
)

// Querier exposes the package-private querier so an external integration test can
// wrap a real tx with fault injection.
type Querier = querier

// SubscriptionCreateAndLink exposes the package-private create+link core so the
// rollback-injection test can drive it against a faulted querier.
func SubscriptionCreateAndLink(ctx context.Context, q Querier, sub contracts.Subscription, tokenHash, tokenPrefix, userID string) error {
	return subscriptionCreateAndLink(ctx, q, sub, tokenHash, tokenPrefix, userID)
}

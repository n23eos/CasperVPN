package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
)

// SubscriptionStore persists entitlements. NO card/payment data — hashed token only.
type SubscriptionStore struct {
	q    querier
	pool *pgxpool.Pool // for CreateAndLink's cross-table transaction
}

// NewSubscriptionStore builds a SubscriptionStore.
func NewSubscriptionStore(pool *pgxpool.Pool) *SubscriptionStore {
	return &SubscriptionStore{q: pool, pool: pool}
}

// CreateAndLink inserts the subscription and links it onto the user in ONE
// transaction, locking the user row (FOR UPDATE) and rejecting with ErrConflict if
// the user already has a subscription — closing the duplicate-create race and the
// orphan window in one atomic step.
func (s *SubscriptionStore) CreateAndLink(ctx context.Context, sub contracts.Subscription, tokenHash, tokenPrefix, userID string) error {
	return withTx(ctx, s.pool, func(q querier) error {
		return subscriptionCreateAndLink(ctx, q, sub, tokenHash, tokenPrefix, userID)
	})
}

// subscriptionCreateAndLink runs the locked create+link on any querier (pool or tx).
// Extracted so a fault-injection test can drive it against a wrapped tx and prove
// that a failure between the two writes leaves neither (no orphan).
func subscriptionCreateAndLink(ctx context.Context, q querier, sub contracts.Subscription, tokenHash, tokenPrefix, userID string) error {
	var existing *string
	err := q.QueryRow(ctx, `SELECT subscription_id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: lock user: %w", err)
	}
	if existing != nil && *existing != "" {
		return domain.ErrConflict
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO subscriptions
			(id, user_id, plan, status, token_hash, token_prefix,
			 traffic_limit_bytes, speed_limit_mbps, device_limit, starts_at, expires_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		sub.ID, sub.UserID, string(sub.Plan), string(sub.Status), tokenHash, tokenPrefix,
		int64(sub.TrafficLimitBytes), sub.SpeedLimitMbps, sub.DeviceLimit,
		sub.StartsAt, sub.ExpiresAt, sub.CreatedAt, sub.UpdatedAt); err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return fmt.Errorf("postgres: insert subscription: %w", err)
	}
	ct, err := q.Exec(ctx, `UPDATE users SET subscription_id=$2, updated_at=$3 WHERE id=$1`,
		userID, sub.ID, sub.UpdatedAt)
	if err != nil {
		return fmt.Errorf("postgres: link subscription: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Create persists a subscription, storing only the token HASH.
func (s *SubscriptionStore) Create(ctx context.Context, sub contracts.Subscription, tokenHash, tokenPrefix string) error {
	_, err := s.q.Exec(ctx, `
		INSERT INTO subscriptions
			(id, user_id, plan, status, token_hash, token_prefix,
			 traffic_limit_bytes, speed_limit_mbps, device_limit, starts_at, expires_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		sub.ID, sub.UserID, string(sub.Plan), string(sub.Status), tokenHash, tokenPrefix,
		int64(sub.TrafficLimitBytes), sub.SpeedLimitMbps, sub.DeviceLimit,
		sub.StartsAt, sub.ExpiresAt, sub.CreatedAt, sub.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return fmt.Errorf("postgres: insert subscription: %w", err)
	}
	return nil
}

// Update persists mutable entitlement fields (status, expires_at, updated_at).
// The token hash is not touched here — see UpdateTokenHash.
func (s *SubscriptionStore) Update(ctx context.Context, sub contracts.Subscription) error {
	tag, err := s.q.Exec(ctx, `
		UPDATE subscriptions
		SET status=$2, expires_at=$3, updated_at=$4
		WHERE id=$1`,
		sub.ID, string(sub.Status), sub.ExpiresAt, sub.UpdatedAt)
	if err != nil {
		return fmt.Errorf("postgres: update subscription: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// UpdateTokenHash swaps the stored token hash/prefix (rotation). Plaintext
// never reaches this layer.
func (s *SubscriptionStore) UpdateTokenHash(ctx context.Context, id, tokenHash, tokenPrefix string) error {
	tag, err := s.q.Exec(ctx, `
		UPDATE subscriptions
		SET token_hash=$2, token_prefix=$3
		WHERE id=$1`, id, tokenHash, tokenPrefix)
	if err != nil {
		return fmt.Errorf("postgres: rotate subscription token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Get returns a subscription by id. The token is never selected/exposed.
func (s *SubscriptionStore) Get(ctx context.Context, id string) (contracts.Subscription, error) {
	var (
		sub     contracts.Subscription
		plan    string
		status  string
		traffic int64
	)
	err := s.q.QueryRow(ctx, `
		SELECT id, user_id, plan, status, traffic_limit_bytes, speed_limit_mbps,
			device_limit, starts_at, expires_at, created_at, updated_at
		FROM subscriptions WHERE id=$1`, id).Scan(
		&sub.ID, &sub.UserID, &plan, &status, &traffic, &sub.SpeedLimitMbps,
		&sub.DeviceLimit, &sub.StartsAt, &sub.ExpiresAt, &sub.CreatedAt, &sub.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.Subscription{}, domain.ErrNotFound
		}
		return contracts.Subscription{}, fmt.Errorf("postgres: scan subscription: %w", err)
	}
	sub.Plan = contracts.SubscriptionPlan(plan)
	sub.Status = contracts.SubscriptionStatus(status)
	sub.TrafficLimitBytes = uint64(traffic)
	return sub, nil
}

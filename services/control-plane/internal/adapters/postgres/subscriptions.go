package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubscriptionStore persists entitlements. NO card/payment data — hashed token only.
type SubscriptionStore struct {
	q querier
}

// NewSubscriptionStore builds a SubscriptionStore.
func NewSubscriptionStore(pool *pgxpool.Pool) *SubscriptionStore { return &SubscriptionStore{q: pool} }

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

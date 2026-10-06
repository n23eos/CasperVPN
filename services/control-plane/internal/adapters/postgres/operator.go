package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/contracts"
)

// OperatorReader obtains a bounded private overview from one consistent snapshot.
type OperatorReader struct{ pool *pgxpool.Pool }

func NewOperatorReader(pool *pgxpool.Pool) *OperatorReader { return &OperatorReader{pool: pool} }

func (s *OperatorReader) Read(ctx context.Context) (contracts.ControlPlaneOperatorSummary, error) {
	result := contracts.ControlPlaneOperatorSummary{
		Users:         map[contracts.UserStatus]int64{contracts.UserStatusActive: 0, contracts.UserStatusSuspended: 0, contracts.UserStatusExpired: 0, contracts.UserStatusBanned: 0},
		Subscriptions: map[contracts.SubscriptionStatus]int64{contracts.SubscriptionStatusTrialing: 0, contracts.SubscriptionStatusActive: 0, contracts.SubscriptionStatusPastDue: 0, contracts.SubscriptionStatusCanceled: 0, contracts.SubscriptionStatusExpired: 0},
		Recent:        []contracts.OperatorAccount{},
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, "SELECT status, count(*) FROM users GROUP BY status")
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var status contracts.UserStatus
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return result, err
		}
		result.Users[status] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.Query(ctx, "SELECT status, count(*) FROM subscriptions GROUP BY status")
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var status contracts.SubscriptionStatus
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return result, err
		}
		result.Subscriptions[status] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.Query(ctx, `SELECT u.id::text,u.status,u.subscription_id::text,
  COALESCE(s.plan,''),COALESCE(s.status,''),s.expires_at,s.grace_until,u.updated_at
  FROM users u LEFT JOIN subscriptions s ON s.id=u.subscription_id AND s.user_id=u.id
  ORDER BY u.updated_at DESC,u.id DESC LIMIT 50`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var account contracts.OperatorAccount
		if err := rows.Scan(&account.ID, &account.Status, &account.SubscriptionID, &account.Plan, &account.SubscriptionStatus, &account.ExpiresAt, &account.GraceUntil, &account.UpdatedAt); err != nil {
			rows.Close()
			return result, err
		}
		result.Recent = append(result.Recent, account)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}

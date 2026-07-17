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

const userColumns = `id, telegram_id, email, status, reality_short_id, vless_uuid,
	private_key, subscription_id, device_limit, quota_bytes, used_bytes, created_at, updated_at`

// UserStore persists accounts and their personal isolation secrets.
type UserStore struct {
	q querier
}

// NewUserStore builds a UserStore.
func NewUserStore(pool *pgxpool.Pool) *UserStore { return &UserStore{q: pool} }

// Create inserts a user with their personal isolation secrets.
func (s *UserStore) Create(ctx context.Context, u contracts.User) error {
	_, err := s.q.Exec(ctx, `
		INSERT INTO users (`+userColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		u.ID, u.TelegramID, u.Email, string(u.Status), u.RealityShortID, u.UUID,
		u.PrivateKey, u.SubscriptionID, u.DeviceLimit, int64(u.QuotaBytes), int64(u.UsedBytes),
		u.CreatedAt, u.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return fmt.Errorf("postgres: insert user: %w", err)
	}
	return nil
}

// Get returns a user by id.
func (s *UserStore) Get(ctx context.Context, id string) (contracts.User, error) {
	return scanUser(s.q.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1`, id))
}

// Update writes mutable user fields (not the secrets).
func (s *UserStore) Update(ctx context.Context, u contracts.User) error {
	ct, err := s.q.Exec(ctx, `
		UPDATE users SET telegram_id=$2, email=$3, status=$4, subscription_id=$5,
			device_limit=$6, quota_bytes=$7, used_bytes=$8, updated_at=$9
		WHERE id=$1`,
		u.ID, u.TelegramID, u.Email, string(u.Status), u.SubscriptionID,
		u.DeviceLimit, int64(u.QuotaBytes), int64(u.UsedBytes), u.UpdatedAt)
	if err != nil {
		return fmt.Errorf("postgres: update user: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// RotateSecrets swaps the personal secrets atomically and returns the new user.
func (s *UserStore) RotateSecrets(ctx context.Context, id, shortID, uuid, privKey string) (contracts.User, error) {
	row := s.q.QueryRow(ctx, `
		UPDATE users SET reality_short_id=$2, vless_uuid=$3, private_key=$4, updated_at=now()
		WHERE id=$1 RETURNING `+userColumns, id, shortID, uuid, privKey)
	return scanUser(row)
}

// AllActiveIDs lists ids of active users (rebuild candidates).
func (s *UserStore) AllActiveIDs(ctx context.Context) ([]string, error) {
	rows, err := s.q.Query(ctx, `SELECT id FROM users WHERE status='active'`)
	if err != nil {
		return nil, fmt.Errorf("postgres: active user ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// EligibleRealityUsers returns the (uuid, short_id) of every user a node must
// admit: account active AND its current subscription servable (active/trialing/
// past_due) AND not past expires_at. This mirrors the subscription service's
// resolve eligibility exactly, so a client that gets a working config is always
// present in the node allow-list. Ordered by uuid for a stable digest upstream.
func (s *UserStore) EligibleRealityUsers(ctx context.Context) ([]contracts.RealityUser, error) {
	return queryEligibleRealityUsers(ctx, s.q)
}

const eligibleRealityUsersSQL = `
SELECT u.vless_uuid, u.reality_short_id
FROM users u
JOIN subscriptions sub ON sub.id = u.subscription_id
WHERE u.status = 'active'
  AND sub.status IN ('active', 'trialing', 'past_due')
  AND (sub.expires_at IS NULL OR sub.expires_at > now())
ORDER BY u.vless_uuid`

// forUpdate locks the eligible users' and subscriptions' rows. Activation uses
// this variant so a concurrent ban/rotation (UPDATE users / UPDATE subscriptions)
// blocks until activation commits — SERIALIZABLE alone does not protect a read
// against a READ COMMITTED writer, so the explicit row lock is what actually
// prevents activating with an allow-list that changed mid-transaction.
const eligibleRealityUsersForUpdateSQL = eligibleRealityUsersSQL + `
FOR UPDATE OF u, sub`

// queryEligibleRealityUsers runs the eligibility join on any querier (pool or tx).
func queryEligibleRealityUsers(ctx context.Context, q querier) ([]contracts.RealityUser, error) {
	return scanRealityUsers(q.Query(ctx, eligibleRealityUsersSQL))
}

// queryEligibleRealityUsersForUpdate is the row-locking variant for activation.
func queryEligibleRealityUsersForUpdate(ctx context.Context, q querier) ([]contracts.RealityUser, error) {
	return scanRealityUsers(q.Query(ctx, eligibleRealityUsersForUpdateSQL))
}

func scanRealityUsers(rows pgx.Rows, err error) ([]contracts.RealityUser, error) {
	if err != nil {
		return nil, fmt.Errorf("postgres: eligible reality users: %w", err)
	}
	defer rows.Close()
	var out []contracts.RealityUser
	for rows.Next() {
		var ru contracts.RealityUser
		if err := rows.Scan(&ru.UUID, &ru.ShortID); err != nil {
			return nil, err
		}
		out = append(out, ru)
	}
	return out, rows.Err()
}

func scanUser(row pgx.Row) (contracts.User, error) {
	var (
		u           contracts.User
		status      string
		quota, used int64
	)
	if err := row.Scan(&u.ID, &u.TelegramID, &u.Email, &status, &u.RealityShortID, &u.UUID,
		&u.PrivateKey, &u.SubscriptionID, &u.DeviceLimit, &quota, &used, &u.CreatedAt, &u.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.User{}, domain.ErrNotFound
		}
		return contracts.User{}, fmt.Errorf("postgres: scan user: %w", err)
	}
	u.Status = contracts.UserStatus(status)
	u.QuotaBytes = uint64(quota)
	u.UsedBytes = uint64(used)
	return u, nil
}

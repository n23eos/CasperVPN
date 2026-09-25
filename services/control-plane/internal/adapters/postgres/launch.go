package postgres

import (
	"context"
	"errors"
	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/jackc/pgx/v5"
	"time"
)

func (s *SubscriptionStore) ApplyBillingState(ctx context.Context, id string, state contracts.BillingState) (contracts.Subscription, error) {
	var out contracts.Subscription
	err := withTx(ctx, s.pool, func(q querier) error {
		var locked string
		if err := q.QueryRow(ctx, `SELECT id FROM subscriptions WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}
		txStore := &SubscriptionStore{q: q}
		current, err := txStore.Get(ctx, id)
		if err != nil {
			return err
		}
		if current.BillingRevision == state.Revision {
			if current.Status != state.Status || current.ExpiresAt == nil || !current.ExpiresAt.Equal(state.ExpiresAt) || current.GraceUntil == nil || !current.GraceUntil.Equal(state.GraceUntil) {
				return domain.ErrConflict
			}
		} else if current.BillingRevision < state.Revision {
			if _, err := q.Exec(ctx, `UPDATE subscriptions SET billing_revision=$2,status=$3,expires_at=$4,grace_until=$5,updated_at=now() WHERE id=$1`, id, state.Revision, string(state.Status), state.ExpiresAt, state.GraceUntil); err != nil {
				return err
			}
		}
		out, err = txStore.Get(ctx, id)
		return err
	})
	return out, err
}
func (s *SubscriptionStore) EnsureDeliveryToken(ctx context.Context, id string, candidate domain.DeliveryToken) (domain.DeliveryToken, error) {
	var out domain.DeliveryToken
	err := withTx(ctx, s.pool, func(q querier) error {
		var locked string
		if err := q.QueryRow(ctx, `SELECT id FROM subscriptions WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}
		_, err := q.Exec(ctx, `INSERT INTO subscription_delivery_tokens(subscription_id,token_hash,ciphertext,key_id) VALUES($1,$2,$3,$4) ON CONFLICT(subscription_id) DO NOTHING`, id, candidate.Hash, candidate.Ciphertext, candidate.KeyID)
		if err != nil {
			return err
		}
		return q.QueryRow(ctx, `SELECT token_hash,ciphertext,key_id FROM subscription_delivery_tokens WHERE subscription_id=$1`, id).Scan(&out.Hash, &out.Ciphertext, &out.KeyID)
	})
	return out, err
}
func (s *SubscriptionStore) ResolveTokenHash(ctx context.Context, hash string) (contracts.SubscriptionTokenBinding, error) {
	var b contracts.SubscriptionTokenBinding
	err := s.q.QueryRow(ctx, `SELECT sub.user_id,sub.id FROM subscriptions sub JOIN users u ON u.id=sub.user_id AND u.subscription_id=sub.id WHERE (sub.token_hash=$1 OR EXISTS(SELECT 1 FROM subscription_delivery_tokens d WHERE d.subscription_id=sub.id AND d.token_hash=$1)) AND u.status='active'`, hash).Scan(&b.UserID, &b.SubscriptionID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.ErrNotFound
	}
	return b, err
}

func (s *UserStore) EligibleAccessUsers(ctx context.Context) (contracts.NodeAccessUsers, error) {
	return queryEligibleAccessUsers(ctx, s.q, false)
}
func queryEligibleAccessUsers(ctx context.Context, q querier, lock bool) (contracts.NodeAccessUsers, error) {
	// DB time is used for both filtering and lease deadlines, independent of caller clocks.
	var now time.Time
	if err := q.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return contracts.NodeAccessUsers{}, err
	}
	out := contracts.NodeAccessUsers{Users: []contracts.AccessUser{}, ValidUntil: now.Add(5 * time.Minute)}
	query := `SELECT u.vless_uuid,u.reality_short_id,u.hysteria2_password,COALESCE(sub.grace_until,sub.expires_at) FROM users u JOIN subscriptions sub ON sub.id=u.subscription_id WHERE u.status='active' AND sub.status IN ('active','trialing','past_due') AND (COALESCE(sub.grace_until,sub.expires_at) IS NULL OR COALESCE(sub.grace_until,sub.expires_at)>$1) ORDER BY u.vless_uuid`
	if lock {
		query += ` FOR UPDATE OF u,sub`
	}
	rows, err := q.Query(ctx, query, now)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var u contracts.AccessUser
		var end *time.Time
		if err := rows.Scan(&u.UUID, &u.ShortID, &u.Hysteria2Password, &end); err != nil {
			return out, err
		}
		if u.Hysteria2Password == "" {
			return out, domain.ErrConflict
		}
		out.Users = append(out.Users, u)
		if end != nil && end.Before(out.ValidUntil) {
			out.ValidUntil = *end
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Revision = contracts.AccessUsersRevision(out.Users)
	return out, nil
}

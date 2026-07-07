package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SetStore persists built per-user subscription sets (cache + source for Agent C).
type SetStore struct {
	q querier
}

// NewSetStore builds a SetStore.
func NewSetStore(pool *pgxpool.Pool) *SetStore { return &SetStore{q: pool} }

// Get returns a user's built set, or ErrNotFound.
func (s *SetStore) Get(ctx context.Context, userID string) (domain.SubscriptionSet, error) {
	var (
		set    domain.SubscriptionSet
		bundle []byte
	)
	err := s.q.QueryRow(ctx, `
		SELECT user_id, revision, node_ids, bundle, dirty, generated_at
		FROM subscription_sets WHERE user_id=$1`, userID).Scan(
		&set.UserID, &set.Revision, &set.NodeIDs, &bundle, &set.Dirty, &set.GeneratedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.SubscriptionSet{}, domain.ErrNotFound
		}
		return domain.SubscriptionSet{}, fmt.Errorf("postgres: get set: %w", err)
	}
	var b contracts.SubscriptionBundle
	if err := json.Unmarshal(bundle, &b); err != nil {
		return domain.SubscriptionSet{}, fmt.Errorf("postgres: unmarshal bundle: %w", err)
	}
	set.Bundle = b
	return set, nil
}

// Upsert writes (or replaces) a user's built set.
func (s *SetStore) Upsert(ctx context.Context, set domain.SubscriptionSet) error {
	bundle, err := json.Marshal(set.Bundle)
	if err != nil {
		return fmt.Errorf("postgres: marshal bundle: %w", err)
	}
	nodeIDs := set.NodeIDs
	if nodeIDs == nil {
		nodeIDs = []string{}
	}
	_, err = s.q.Exec(ctx, `
		INSERT INTO subscription_sets (user_id, revision, node_ids, bundle, dirty, generated_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (user_id) DO UPDATE SET
			revision=EXCLUDED.revision, node_ids=EXCLUDED.node_ids, bundle=EXCLUDED.bundle,
			dirty=EXCLUDED.dirty, generated_at=EXCLUDED.generated_at`,
		set.UserID, set.Revision, nodeIDs, bundle, set.Dirty, set.GeneratedAt)
	if err != nil {
		return fmt.Errorf("postgres: upsert set: %w", err)
	}
	return nil
}

// UserIDsByNode returns users whose current set includes the node.
func (s *SetStore) UserIDsByNode(ctx context.Context, nodeID string) ([]string, error) {
	rows, err := s.q.Query(ctx, `SELECT user_id FROM subscription_sets WHERE $1 = ANY(node_ids)`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("postgres: users by node: %w", err)
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

// MarkDirtyByNode invalidates cached sets that include the node.
func (s *SetStore) MarkDirtyByNode(ctx context.Context, nodeID string) error {
	_, err := s.q.Exec(ctx, `UPDATE subscription_sets SET dirty=true WHERE $1 = ANY(node_ids)`, nodeID)
	if err != nil {
		return fmt.Errorf("postgres: mark dirty: %w", err)
	}
	return nil
}

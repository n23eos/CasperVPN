package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
)

// RotationStore persists node and user-secret rotation history.
type RotationStore struct {
	q querier
}

// NewRotationStore builds a RotationStore.
func NewRotationStore(pool *pgxpool.Pool) *RotationStore { return &RotationStore{q: pool} }

// AddNodeRotation appends a node lifecycle history row.
func (s *RotationStore) AddNodeRotation(ctx context.Context, r domain.RotationRecord) error {
	var from *string
	if r.FromStatus != nil {
		v := string(*r.FromStatus)
		from = &v
	}
	_, err := s.q.Exec(ctx, `
		INSERT INTO node_rotation_history (id, node_id, from_status, to_status, reason, entry_ip, actor, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		r.ID, r.NodeID, from, string(r.ToStatus), r.Reason, nullIfEmpty(r.EntryIP), r.Actor, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("postgres: insert node rotation: %w", err)
	}
	return nil
}

// ListNodeRotations returns recent history for a node, newest first.
func (s *RotationStore) ListNodeRotations(ctx context.Context, nodeID string, limit int) ([]domain.RotationRecord, error) {
	rows, err := s.q.Query(ctx, `
		SELECT id, node_id, from_status, to_status, reason, entry_ip, actor, created_at
		FROM node_rotation_history WHERE node_id=$1 ORDER BY created_at DESC LIMIT $2`, nodeID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list node rotations: %w", err)
	}
	defer rows.Close()
	var out []domain.RotationRecord
	for rows.Next() {
		var (
			r       domain.RotationRecord
			from    *string
			entryIP *string
			to      string
		)
		if err := rows.Scan(&r.ID, &r.NodeID, &from, &to, &r.Reason, &entryIP, &r.Actor, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.ToStatus = contracts.NodeStatus(to)
		if from != nil {
			fs := contracts.NodeStatus(*from)
			r.FromStatus = &fs
		}
		if entryIP != nil {
			r.EntryIP = *entryIP
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddUserSecretRotation appends a user secret-rotation history row.
func (s *RotationStore) AddUserSecretRotation(ctx context.Context, r domain.UserSecretRotation) error {
	_, err := s.q.Exec(ctx, `
		INSERT INTO user_secret_rotations (id, user_id, old_reality_short_id, old_vless_uuid, reason, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		r.ID, r.UserID, r.OldRealityShortID, r.OldVlessUUID, r.Reason, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("postgres: insert user secret rotation: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

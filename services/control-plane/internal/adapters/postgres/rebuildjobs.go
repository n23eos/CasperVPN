package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/control-plane/internal/adapters/rebuild"
)

// RebuildJobStore is the durable backend for the rebuild queue (implements
// rebuild.JobStore). It makes pending rebuilds survive a restart and lets
// several control-plane instances share one queue via FOR UPDATE SKIP LOCKED.
//
// Rebuilds are idempotent (a user's set is recomputed from scratch), so the
// store does not coalesce duplicate pending jobs — at-least-once is fine and
// avoids a partial-unique-index conflict on retry.
type RebuildJobStore struct{ q querier }

// NewRebuildJobStore builds the store over a pool.
func NewRebuildJobStore(pool *pgxpool.Pool) *RebuildJobStore { return &RebuildJobStore{q: pool} }

// Compile-time proof the adapter satisfies the port.
var _ rebuild.JobStore = (*RebuildJobStore)(nil)

// Enqueue records a pending job (durable write).
func (s *RebuildJobStore) Enqueue(ctx context.Context, kind rebuild.JobKind, targetID string) error {
	if _, err := s.q.Exec(ctx,
		`INSERT INTO rebuild_jobs (kind, target_id) VALUES ($1, $2)`,
		string(kind), targetID); err != nil {
		return fmt.Errorf("postgres: enqueue rebuild job: %w", err)
	}
	return nil
}

// Claim leases up to max due jobs (available and not already leased within
// leaseFor), bumping attempts. Concurrent workers/instances never take the same
// rows thanks to FOR UPDATE SKIP LOCKED.
func (s *RebuildJobStore) Claim(ctx context.Context, max int, leaseFor time.Duration) ([]rebuild.Job, error) {
	rows, err := s.q.Query(ctx, `
		UPDATE rebuild_jobs SET claimed_at = now(), attempts = attempts + 1
		WHERE id IN (
			SELECT id FROM rebuild_jobs
			WHERE available_at <= now()
			  AND (claimed_at IS NULL OR claimed_at < now() - make_interval(secs => $2))
			ORDER BY id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, kind, target_id, attempts`,
		max, leaseFor.Seconds())
	if err != nil {
		return nil, fmt.Errorf("postgres: claim rebuild jobs: %w", err)
	}
	defer rows.Close()

	var out []rebuild.Job
	for rows.Next() {
		var (
			j    rebuild.Job
			kind string
		)
		if err := rows.Scan(&j.ID, &kind, &j.TargetID, &j.Attempts); err != nil {
			return nil, fmt.Errorf("postgres: scan rebuild job: %w", err)
		}
		j.Kind = rebuild.JobKind(kind)
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate rebuild jobs: %w", err)
	}
	return out, nil
}

// Complete removes a finished (or dropped) job.
func (s *RebuildJobStore) Complete(ctx context.Context, jobID int64) error {
	if _, err := s.q.Exec(ctx, `DELETE FROM rebuild_jobs WHERE id = $1`, jobID); err != nil {
		return fmt.Errorf("postgres: complete rebuild job: %w", err)
	}
	return nil
}

// Retry releases a failed job, making it claimable again after retryAfter.
func (s *RebuildJobStore) Retry(ctx context.Context, jobID int64, retryAfter time.Duration) error {
	if _, err := s.q.Exec(ctx,
		`UPDATE rebuild_jobs SET claimed_at = NULL, available_at = now() + make_interval(secs => $2) WHERE id = $1`,
		jobID, retryAfter.Seconds()); err != nil {
		return fmt.Errorf("postgres: retry rebuild job: %w", err)
	}
	return nil
}

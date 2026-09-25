// Package botstore persists Telegram update progress.
package botstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres stores one durable cursor and the state of each claimed update.
type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

func (s *Postgres) Cursor(ctx context.Context) (int64, error) {
	var cursor int64
	if err := s.pool.QueryRow(ctx,
		"SELECT last_update_id FROM delivery_telegram_cursor WHERE singleton = TRUE").Scan(&cursor); err != nil {
		return 0, fmt.Errorf("bot store: read cursor: %w", err)
	}
	return cursor, nil
}

// Begin claims a new update or resumes a pending one. Completed updates return
// false so a replay cannot repeat business operations.
func (s *Postgres) Begin(ctx context.Context, updateID int64) (bool, error) {
	var status string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO delivery_telegram_updates (update_id, status)
		VALUES ($1, 'pending')
		ON CONFLICT (update_id) DO UPDATE
		SET attempts = delivery_telegram_updates.attempts + 1,
		    updated_at = now()
		RETURNING status`, updateID).Scan(&status)
	if err != nil {
		return false, fmt.Errorf("bot store: claim update: %w", err)
	}
	return status != "done", nil
}

// Complete atomically records completion and advances the polling cursor.
func (s *Postgres) Complete(ctx context.Context, updateID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("bot store: begin complete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE delivery_telegram_updates
		SET status = 'done', updated_at = now()
		WHERE update_id = $1`, updateID); err != nil {
		return fmt.Errorf("bot store: mark complete: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE delivery_telegram_cursor
		SET last_update_id = GREATEST(last_update_id, $1), updated_at = now()
		WHERE singleton = TRUE`, updateID); err != nil {
		return fmt.Errorf("bot store: advance cursor: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("bot store: commit complete: %w", err)
	}
	return nil
}

func (s *Postgres) Ready(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("bot store: database unavailable")
	}
	return nil
}

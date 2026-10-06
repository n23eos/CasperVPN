// Package botstore persists Telegram update progress.
package botstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/delivery/internal/channel/telegram"
)

// Postgres stores one durable cursor and the state of each claimed update.
type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// RecordUser opts a Telegram sender into service notifications only after a
// valid private bot update established sender ID == chat ID.
func (s *Postgres) RecordUser(ctx context.Context, telegramID int64) error {
	if telegramID <= 0 {
		return fmt.Errorf("bot store: positive telegram id required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO delivery_bot_users (telegram_id)
		VALUES ($1)
		ON CONFLICT (telegram_id) DO UPDATE SET last_seen_at = now()`, telegramID)
	if err != nil {
		return fmt.Errorf("bot store: record user: %w", err)
	}
	return nil
}

// NotificationUsers returns a bounded page and advances a durable cursor. Once
// the tail is reached, the next call wraps to the first user so no user beyond
// the batch limit starves across runs or restarts.
func (s *Postgres) NotificationUsers(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("bot store: positive notification limit required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("bot store: begin notification page: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var cursor int64
	if err := tx.QueryRow(ctx, `
		SELECT last_telegram_id FROM delivery_notification_cursor
		WHERE singleton = TRUE FOR UPDATE`).Scan(&cursor); err != nil {
		return nil, fmt.Errorf("bot store: read notification cursor: %w", err)
	}
	read := func(after int64) ([]int64, error) {
		rows, err := tx.Query(ctx, `
			SELECT telegram_id FROM delivery_bot_users
			WHERE telegram_id > $1
			ORDER BY telegram_id
			LIMIT $2`, after, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		users := make([]int64, 0, limit)
		for rows.Next() {
			var telegramID int64
			if err := rows.Scan(&telegramID); err != nil {
				return nil, err
			}
			users = append(users, telegramID)
		}
		return users, rows.Err()
	}
	users, err := read(cursor)
	if err != nil {
		return nil, fmt.Errorf("bot store: list notification users: %w", err)
	}
	if len(users) == 0 && cursor != 0 {
		users, err = read(0)
		if err != nil {
			return nil, fmt.Errorf("bot store: wrap notification users: %w", err)
		}
	}
	next := int64(0)
	if len(users) > 0 {
		next = users[len(users)-1]
	}
	if _, err := tx.Exec(ctx, `
		UPDATE delivery_notification_cursor
		SET last_telegram_id = $1, updated_at = now()
		WHERE singleton = TRUE`, next); err != nil {
		return nil, fmt.Errorf("bot store: advance notification cursor: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("bot store: commit notification page: %w", err)
	}
	return users, nil
}

func (s *Postgres) ReconcileNotifications(ctx context.Context, telegramID int64, current []telegram.Notification, allowActivation bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("bot store: begin notification reconcile: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE delivery_bot_notifications
		SET status = 'canceled', updated_at = now()
		WHERE telegram_id = $1 AND status = 'pending' AND kind IN ('expiry', 'grace')`, telegramID); err != nil {
		return fmt.Errorf("bot store: cancel stale reminders: %w", err)
	}
	if !allowActivation {
		if _, err := tx.Exec(ctx, `
			UPDATE delivery_bot_notifications
			SET status = 'canceled', updated_at = now()
			WHERE telegram_id = $1 AND status = 'pending' AND kind = 'activation'`, telegramID); err != nil {
			return fmt.Errorf("bot store: cancel stale activation: %w", err)
		}
	}
	for _, notification := range current {
		if notification.EventKey == "" || notification.Text == "" ||
			(notification.Kind != "activation" && notification.Kind != "expiry" && notification.Kind != "grace") {
			return fmt.Errorf("bot store: invalid notification")
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO delivery_bot_notifications (telegram_id, event_key, kind, message, status)
			VALUES ($1, $2, $3, $4, 'pending')
			ON CONFLICT (telegram_id, event_key) DO UPDATE
			SET kind = EXCLUDED.kind,
			    message = EXCLUDED.message,
			    status = CASE
			        WHEN delivery_bot_notifications.status = 'sent' THEN 'sent'
			        ELSE 'pending'
			    END,
			    updated_at = now()`, telegramID, notification.EventKey, notification.Kind, notification.Text)
		if err != nil {
			return fmt.Errorf("bot store: stage notification: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("bot store: commit notification reconcile: %w", err)
	}
	return nil
}

func (s *Postgres) PendingNotifications(ctx context.Context, telegramID int64, limit int) ([]telegram.Notification, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("bot store: positive pending notification limit required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT event_key, kind, message
		FROM delivery_bot_notifications
		WHERE telegram_id = $1 AND status = 'pending'
		ORDER BY created_at, event_key
		LIMIT $2`, telegramID, limit)
	if err != nil {
		return nil, fmt.Errorf("bot store: list pending notifications: %w", err)
	}
	defer rows.Close()
	notifications := make([]telegram.Notification, 0)
	for rows.Next() {
		var notification telegram.Notification
		if err := rows.Scan(&notification.EventKey, &notification.Kind, &notification.Text); err != nil {
			return nil, fmt.Errorf("bot store: scan pending notification: %w", err)
		}
		notifications = append(notifications, notification)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bot store: iterate pending notifications: %w", err)
	}
	return notifications, nil
}

func (s *Postgres) MarkNotificationSent(ctx context.Context, telegramID int64, eventKey string) error {
	if eventKey == "" {
		return fmt.Errorf("bot store: notification event key required")
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE delivery_bot_notifications
		SET status = 'sent', sent_at = now(), updated_at = now()
		WHERE telegram_id = $1 AND event_key = $2 AND status = 'pending'`, telegramID, eventKey)
	if err != nil {
		return fmt.Errorf("bot store: mark notification: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("bot store: pending notification not found")
	}
	return nil
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

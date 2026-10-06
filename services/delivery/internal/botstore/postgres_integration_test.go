//go:build integration

package botstore

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/delivery/internal/channel/telegram"
	"github.com/caspervpn/delivery/internal/migrate"
)

func TestPostgresDedupSurvivesRestart(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE delivery_telegram_updates"); err != nil {
		t.Fatalf("reset updates: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE delivery_telegram_cursor SET last_update_id = 0"); err != nil {
		t.Fatalf("reset: %v", err)
	}

	first := NewPostgres(pool)
	process, err := first.Begin(ctx, 101)
	if err != nil || !process {
		t.Fatalf("first claim = %v, %v", process, err)
	}
	if err := first.Complete(ctx, 101); err != nil {
		t.Fatalf("complete: %v", err)
	}

	restarted := NewPostgres(pool)
	cursor, err := restarted.Cursor(ctx)
	if err != nil || cursor != 101 {
		t.Fatalf("cursor after restart = %d, %v", cursor, err)
	}
	process, err = restarted.Begin(ctx, 101)
	if err != nil || process {
		t.Fatalf("completed replay = %v, %v", process, err)
	}
}

func TestNotificationPagingAndMarkersSurviveRestart(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE delivery_bot_notifications, delivery_bot_users"); err != nil {
		t.Fatalf("reset users: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE delivery_notification_cursor SET last_telegram_id = 0"); err != nil {
		t.Fatalf("reset cursor: %v", err)
	}
	store := NewPostgres(pool)
	for _, telegramID := range []int64{10, 20, 30} {
		if err := store.RecordUser(ctx, telegramID); err != nil {
			t.Fatalf("RecordUser(%d): %v", telegramID, err)
		}
	}
	first, err := store.NotificationUsers(ctx, 2)
	if err != nil || len(first) != 2 || first[0] != 10 || first[1] != 20 {
		t.Fatalf("first page = %v, %v", first, err)
	}
	restarted := NewPostgres(pool)
	second, err := restarted.NotificationUsers(ctx, 2)
	if err != nil || len(second) != 1 || second[0] != 30 {
		t.Fatalf("page after restart = %v, %v", second, err)
	}
	wrapped, err := restarted.NotificationUsers(ctx, 2)
	if err != nil || len(wrapped) != 2 || wrapped[0] != 10 || wrapped[1] != 20 {
		t.Fatalf("wrapped page = %v, %v", wrapped, err)
	}
	activation := telegram.Notification{EventKey: "activation:inv-1:sub-1", Kind: "activation", Text: "active"}
	expiry := telegram.Notification{EventKey: "expiry:sub-1:123", Kind: "expiry", Text: "expires"}
	if err := store.ReconcileNotifications(ctx, 10, []telegram.Notification{activation, expiry}, true); err != nil {
		t.Fatalf("stage notifications: %v", err)
	}
	if err := restarted.ReconcileNotifications(ctx, 10, nil, true); err != nil {
		t.Fatalf("reconcile pending after restart: %v", err)
	}
	pending, err := restarted.PendingNotifications(ctx, 10, 10)
	if err != nil || len(pending) != 1 || pending[0].EventKey != activation.EventKey {
		t.Fatalf("pending after retry reconcile = %v, %v", pending, err)
	}
	if err := store.MarkNotificationSent(ctx, 10, activation.EventKey); err != nil {
		t.Fatalf("mark notification: %v", err)
	}
	if err := restarted.ReconcileNotifications(ctx, 10, []telegram.Notification{activation}, true); err != nil {
		t.Fatalf("reconcile after restart: %v", err)
	}
	pending, err = restarted.PendingNotifications(ctx, 10, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after restart = %v, %v", pending, err)
	}
}

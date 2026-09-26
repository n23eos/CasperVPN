//go:build integration

package botstore

import (
	"context"
	"os"
	"testing"

	"github.com/caspervpn/delivery/internal/migrate"
	"github.com/jackc/pgx/v5/pgxpool"
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

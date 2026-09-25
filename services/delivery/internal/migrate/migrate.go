// Package migrate applies delivery-owned PostgreSQL migrations at startup.
package migrate

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.up.sql
var migrationsFS embed.FS

const advisoryLockKey int64 = 4771123303

// Up applies all delivery migrations once while holding a service-specific lock.
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("delivery migrate: acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("delivery migrate: acquire lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey) }()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS delivery_schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("delivery migrate: ensure version table: %w", err)
	}

	applied := make(map[string]bool)
	rows, err := conn.Query(ctx, "SELECT version FROM delivery_schema_migrations")
	if err != nil {
		return fmt.Errorf("delivery migrate: read versions: %w", err)
	}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("delivery migrate: scan version: %w", err)
		}
		applied[version] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("delivery migrate: iterate versions: %w", err)
	}

	files, err := migrationFiles()
	if err != nil {
		return err
	}
	for _, file := range files {
		version := strings.TrimSuffix(file, ".up.sql")
		if applied[version] {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + file)
		if err != nil {
			return fmt.Errorf("delivery migrate: read %s: %w", file, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("delivery migrate: begin %s: %w", file, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("delivery migrate: apply %s: %w", file, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO delivery_schema_migrations (version) VALUES ($1)", version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("delivery migrate: record %s: %w", file, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("delivery migrate: commit %s: %w", file, err)
		}
	}
	return nil
}

func migrationFiles() ([]string, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("delivery migrate: list migrations: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

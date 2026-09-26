//go:build integration

package migrate

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationSchemaAndMarkerAreAtomic(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("REQUIRE_INTEGRATION_DB") == "true" {
			t.Fatal("TEST_DATABASE_URL required")
		}
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	schema := fmt.Sprintf("cp_migrate_test_%d", time.Now().UnixNano())
	if _, err = conn.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) }()
	if _, err = conn.Exec(ctx, `SET search_path TO `+schema); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `CREATE TABLE schema_migrations(version TEXT PRIMARY KEY); INSERT INTO schema_migrations VALUES('collision')`); err != nil {
		t.Fatal(err)
	}
	// Fail exactly at marker insertion after successful DDL, the former crash gap.
	if err = applyMigration(ctx, conn, "collision", `BEGIN; CREATE TABLE rollback_probe(id INT); COMMIT;`); err == nil {
		t.Fatal("fault injection did not fail")
	}
	var relation *string
	if err = conn.QueryRow(ctx, `SELECT to_regclass('rollback_probe')::text`).Scan(&relation); err != nil {
		t.Fatal(err)
	}
	if relation != nil {
		t.Fatal("failed version record left committed schema")
	}
	if err = applyMigration(ctx, conn, "ok", `BEGIN; CREATE TABLE rollback_probe(id INT); COMMIT;`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version='ok'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("schema applied without marker", err)
	}
	if err = applyMigration(ctx, conn, "bad_sql", `BEGIN; CREATE TABLE partial_probe(id INT); SELECT missing_column FROM nonexistent_table; COMMIT;`); err == nil {
		t.Fatal("invalid migration accepted")
	}
	if err = conn.QueryRow(ctx, `SELECT to_regclass('partial_probe')::text`).Scan(&relation); err != nil {
		t.Fatal(err)
	}
	if relation != nil {
		t.Fatal("partial DDL survived failed migration")
	}
}

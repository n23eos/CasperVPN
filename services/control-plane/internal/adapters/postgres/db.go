// Package postgres implements the domain repositories on Postgres via pgx. Each
// aggregate has its own store type (Go has no method overloading, so Node.Create
// and User.Create cannot live on one type).
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// querier is the subset of pgx used by the repos. Both *pgxpool.Pool and pgx.Tx
// satisfy it, so a query can run inside or outside a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Connect opens a pgx pool against dsn and verifies connectivity.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: new pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}

// withTx runs fn inside a transaction, passing the tx as the querier.
func withTx(ctx context.Context, pool *pgxpool.Pool, fn func(q querier) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

// withSerializableTx runs fn in a SERIALIZABLE transaction. Under SERIALIZABLE a
// concurrent transaction that mutates rows this one read (e.g. a ban/rotation
// changing eligibility after the digest is computed but before this commits)
// causes a serialization failure (SQLSTATE 40001) at commit — so a stale-read
// activation cannot slip through. The caller maps that to a conflict.
func withSerializableTx(ctx context.Context, pool *pgxpool.Pool, fn func(q querier) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("postgres: begin serializable: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

// isSerializationFailure reports whether err is a Postgres serialization failure
// (SQLSTATE 40001) — the concurrent-conflict signal under SERIALIZABLE.
func isSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40001"
}

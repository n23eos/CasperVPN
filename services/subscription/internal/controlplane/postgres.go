package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Postgres is a durable TokenIndex backed by database/sql. It implements the
// TokenIndex role ONLY (the Provider role stays with the HTTP client); the
// subscription service owns this token->(user,subscription) mapping.
//
// At-rest safety: only token HASHES are stored (HashToken, hex SHA-256), never
// plaintext — the index is exactly the kind of data a seized instance would
// leak (TZ-token-revocation §3). Plaintext exists only in flight (URL/Register).
//
// Schema lives in schema.sql and is applied OUT OF BAND (migration job), never
// on startup, to avoid a migrate-on-boot race between instances.
type Postgres struct{ db *sql.DB }

// NewPostgres wraps an already-open *sql.DB. In main, blank-import a driver
// (github.com/jackc/pgx/v5/stdlib), sql.Open("pgx", DATABASE_URL) and pass it here.
func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

// Compile-time proof that Postgres satisfies the TokenIndex port.
var _ TokenIndex = (*Postgres)(nil)

// Lookup implements TokenIndex. The plaintext token is hashed before the query;
// only the hash is ever sent to the database.
func (p *Postgres) Lookup(ctx context.Context, token string) (string, string, error) {
	const q = `SELECT user_id, subscription_id FROM subscription_tokens WHERE token_hash = $1`
	var userID, subID string
	err := p.db.QueryRowContext(ctx, q, HashToken(token)).Scan(&userID, &subID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", ErrNotFound
	case err != nil:
		return "", "", fmt.Errorf("controlplane: lookup token: %w", err)
	}
	return userID, subID, nil
}

// Register implements TokenIndex, upserting by token hash. A token maps to
// exactly one (user, subscription); re-registering the same token updates it.
func (p *Postgres) Register(ctx context.Context, token, userID, subID string) error {
	const q = `INSERT INTO subscription_tokens (token_hash, user_id, subscription_id)
VALUES ($1, $2, $3)
ON CONFLICT (token_hash) DO UPDATE
	SET user_id = EXCLUDED.user_id, subscription_id = EXCLUDED.subscription_id`
	if _, err := p.db.ExecContext(ctx, q, HashToken(token), userID, subID); err != nil {
		return fmt.Errorf("controlplane: register token: %w", err)
	}
	return nil
}

// Revoke implements TokenIndex (leaked-link revocation). Deleting an absent
// token is not an error — revocation is idempotent.
func (p *Postgres) Revoke(ctx context.Context, token string) error {
	const q = `DELETE FROM subscription_tokens WHERE token_hash = $1`
	if _, err := p.db.ExecContext(ctx, q, HashToken(token)); err != nil {
		return fmt.Errorf("controlplane: revoke token: %w", err)
	}
	return nil
}

// RevokeByUser implements TokenIndex (ban/suspend: every link of the user dies).
func (p *Postgres) RevokeByUser(ctx context.Context, userID string) (int, error) {
	return p.deleteWhere(ctx, `DELETE FROM subscription_tokens WHERE user_id = $1`, userID)
}

// RevokeBySubscription implements TokenIndex (cancel/expire/rotate: only this
// subscription's link dies; the user's other tokens are untouched).
func (p *Postgres) RevokeBySubscription(ctx context.Context, subID string) (int, error) {
	return p.deleteWhere(ctx, `DELETE FROM subscription_tokens WHERE subscription_id = $1`, subID)
}

// deleteWhere runs a single-arg DELETE and returns the rows removed.
func (p *Postgres) deleteWhere(ctx context.Context, q, arg string) (int, error) {
	res, err := p.db.ExecContext(ctx, q, arg)
	if err != nil {
		return 0, fmt.Errorf("controlplane: revoke: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // some drivers don't report affected rows; not fatal
	}
	return int(n), nil
}

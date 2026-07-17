package store

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/billing/internal/idgen"
	"github.com/caspervpn/billing/internal/model"
)

// advisoryLockNamespace namespaces billing's per-user advisory locks (the first
// pg_advisory_lock arg) so they can't collide with any other subsystem's locks.
const advisoryLockNamespace int32 = 0x0B111 // "BILL"

// unlockTimeout bounds the best-effort unlock so a canceled request context can't
// skip releasing the lock.
const unlockTimeout = 5 * time.Second

// Postgres is a durable Repository backed by Postgres via a pgx connection pool.
// It is the production drop-in for Memory: state survives restarts, and the
// settlement latch is enforced by a primary-key constraint so crediting an invoice
// stays at-most-once even across processes. Apply internal/store/schema.sql first.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres wraps an existing pgx pool. The caller owns the pool lifecycle
// (construction from DATABASE_URL and Close) so the store stays a thin data layer.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// WithUserLock holds a session-scoped advisory lock for userID on ONE dedicated
// connection for the whole of fn, so the entire activation of one user is serialized
// across every billing instance sharing this database.
//
// Pool sizing: this holds one pooled connection for all of fn, during which the
// caller (the activator) acquires OTHER pooled connections for its schedule reads/
// writes. Size the pool comfortably above peak concurrent distinct-user activations
// (≈ 2×) so activations don't starve each other waiting for a query connection while
// holding a lock connection. Starvation surfaces as a ctx timeout, never a deadlock.
func (p *Postgres) WithUserLock(ctx context.Context, userID string, fn func(ctx context.Context) error) error {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("billing: acquire lock connection: %w", err)
	}
	key := int32(crc32.ChecksumIEEE([]byte(userID)))
	// Blocking acquire, bounded by ctx: pgx cancels the query if the request context
	// is done, so a caller timeout can't wedge forever waiting on the lock.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1, $2)`, advisoryLockNamespace, key); err != nil {
		conn.Release()
		return fmt.Errorf("billing: advisory lock: %w", err)
	}
	defer func() {
		// Unlock on the SAME session, with a fresh context so an already-canceled
		// request ctx cannot skip the release.
		uctx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
		defer cancel()
		if _, uerr := conn.Exec(uctx, `SELECT pg_advisory_unlock($1, $2)`, advisoryLockNamespace, key); uerr != nil {
			// The connection may still hold the lock — it MUST NOT go back to the pool.
			// Force-closing the backend makes Postgres drop the session-level lock.
			_ = conn.Conn().Close(context.Background())
		}
		conn.Release() // a closed conn is discarded by the pool, not reused
	}()
	return fn(ctx)
}

// Compile-time proof the Postgres store implements the full Repository contract.
var _ Repository = (*Postgres)(nil)

// CreateInvoice inserts a new invoice.
func (p *Postgres) CreateInvoice(ctx context.Context, inv model.Invoice) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO invoices (
			id, provider, anon_user_id, plan, currency, amount,
			pay_address, provider_invoice_id, status, created_at, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		inv.ID, inv.Provider, inv.AnonUserID, inv.Plan, inv.Currency, inv.Amount,
		inv.PayAddress, inv.ProviderInvoiceID, string(inv.Status), inv.CreatedAt, inv.ExpiresAt,
	)
	return err
}

// GetInvoice loads an invoice by id, returning ErrNotFound on a miss.
func (p *Postgres) GetInvoice(ctx context.Context, id string) (model.Invoice, error) {
	row := p.pool.QueryRow(ctx, `
		SELECT id, provider, anon_user_id, plan, currency, amount,
		       pay_address, provider_invoice_id, status, created_at, expires_at
		FROM invoices WHERE id = $1`, id)
	return scanInvoice(row)
}

// SetInvoiceStatus updates an invoice's status, returning ErrNotFound if absent.
func (p *Postgres) SetInvoiceStatus(ctx context.Context, id string, s model.Status) error {
	tag, err := p.pool.Exec(ctx, `UPDATE invoices SET status = $2 WHERE id = $1`, id, string(s))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// OpenInvoices returns every pending invoice.
func (p *Postgres) OpenInvoices(ctx context.Context) ([]model.Invoice, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, provider, anon_user_id, plan, currency, amount,
		       pay_address, provider_invoice_id, status, created_at, expires_at
		FROM invoices WHERE status = $1`, string(model.StatusPending))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Invoice
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// SeenEvent reports whether a (provider, externalID) delivery was recorded.
func (p *Postgres) SeenEvent(ctx context.Context, provider, externalID string) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM seen_events WHERE provider = $1 AND external_id = $2)`,
		provider, externalID).Scan(&exists)
	return exists, err
}

// RecordEvent marks a delivery processed. A replay of the same delivery is a no-op.
func (p *Postgres) RecordEvent(ctx context.Context, provider, externalID string) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO seen_events (provider, external_id) VALUES ($1,$2)
		ON CONFLICT DO NOTHING`, provider, externalID)
	return err
}

// ClaimSettlement atomically claims the right to credit invoiceID. The primary-key
// conflict makes the first insert win; every later caller inserts nothing and gets
// false — this is the at-most-once credit latch.
func (p *Postgres) ClaimSettlement(ctx context.Context, invoiceID string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		INSERT INTO settlements (invoice_id) VALUES ($1)
		ON CONFLICT DO NOTHING`, invoiceID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReleaseSettlement backs out a claim so a later retry can credit the invoice.
func (p *Postgres) ReleaseSettlement(ctx context.Context, invoiceID string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM settlements WHERE invoice_id = $1`, invoiceID)
	return err
}

// MarkSettlementActivated stamps activated_at so recovery finishes without re-activating.
func (p *Postgres) MarkSettlementActivated(ctx context.Context, invoiceID string) error {
	_, err := p.pool.Exec(ctx, `
		UPDATE settlements SET activated_at = now()
		WHERE invoice_id = $1 AND activated_at IS NULL`, invoiceID)
	return err
}

// LeaseStuckSettlements atomically leases up to limit claimed-but-unsettled invoices
// older than olderThan and not currently leased. FOR UPDATE SKIP LOCKED means two
// reconcilers lease disjoint sets and never process the same invoice concurrently; a
// crashed lease expires (reconcile_leased_until <= now) and is retaken.
func (p *Postgres) LeaseStuckSettlements(ctx context.Context, olderThan time.Time, leaseFor time.Duration, limit int) ([]StuckSettlement, error) {
	rows, err := p.pool.Query(ctx, `
		UPDATE settlements SET reconcile_leased_until = now() + make_interval(secs => $2)
		WHERE invoice_id IN (
			SELECT s.invoice_id FROM settlements s
			JOIN invoices i ON i.id = s.invoice_id
			WHERE i.status = 'pending'
			  AND s.claimed_at <= $1
			  AND (s.reconcile_leased_until IS NULL OR s.reconcile_leased_until <= now())
			ORDER BY s.claimed_at
			FOR UPDATE SKIP LOCKED
			LIMIT $3
		)
		RETURNING invoice_id, (activated_at IS NOT NULL)`,
		olderThan, leaseFor.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StuckSettlement
	for rows.Next() {
		var s StuckSettlement
		if err := rows.Scan(&s.InvoiceID, &s.Activated); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ExpireOverdue transitions overdue pending invoices to expired in one statement,
// excluding any invoice that carries a settlement claim (NOT EXISTS) so a paid
// invoice mid-recovery is never buried — no per-invoice race, no N+1.
func (p *Postgres) ExpireOverdue(ctx context.Context, now time.Time, onchainProviders []string, grace time.Duration) error {
	if onchainProviders == nil {
		onchainProviders = []string{} // nil would encode as SQL NULL → provider = ANY(NULL) is NULL, not false
	}
	_, err := p.pool.Exec(ctx, `
		UPDATE invoices i SET status = 'expired'
		WHERE i.status = 'pending'
		  AND (
		        ( NOT (i.provider = ANY($2)) AND $1 > i.expires_at )
		     OR ( i.provider = ANY($2)
		          AND $1 > i.expires_at + make_interval(secs => $3)
		          AND i.last_negative_check_at IS NOT NULL
		          AND i.last_negative_check_at >= i.expires_at + make_interval(secs => $3) )
		      )
		  AND NOT EXISTS (SELECT 1 FROM settlements s WHERE s.invoice_id = i.id)
		  AND NOT EXISTS (SELECT 1 FROM poll_leases pl WHERE pl.invoice_id = i.id AND pl.lease_until > $1)`,
		now, onchainProviders, grace.Seconds())
	return err
}

// AcquirePollLease mints a fresh token and takes the invoice's poll lease, reclaiming
// an existing lease only if it has lapsed (lease_until <= now). No row returned means
// an active lease is held by someone else.
func (p *Postgres) AcquirePollLease(ctx context.Context, invoiceID, owner string, leaseFor time.Duration) (string, bool, error) {
	token := idgen.New()
	var got string
	err := p.pool.QueryRow(ctx, `
		INSERT INTO poll_leases (invoice_id, lease_token, owner, lease_until)
		VALUES ($1, $2, $3, now() + make_interval(secs => $4))
		ON CONFLICT (invoice_id) DO UPDATE
		   SET lease_token = EXCLUDED.lease_token, owner = EXCLUDED.owner, lease_until = EXCLUDED.lease_until
		   WHERE poll_leases.lease_until <= now()
		RETURNING lease_token`,
		invoiceID, token, owner, leaseFor.Seconds()).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("billing: acquire poll lease: %w", err)
	}
	return got, true, nil
}

// RenewPollLease extends the lease only if token still owns it (CAS).
func (p *Postgres) RenewPollLease(ctx context.Context, invoiceID, token string, leaseFor time.Duration) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE poll_leases SET lease_until = now() + make_interval(secs => $3)
		WHERE invoice_id = $1 AND lease_token = $2`,
		invoiceID, token, leaseFor.Seconds())
	if err != nil {
		return false, fmt.Errorf("billing: renew poll lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReleasePollLease drops the lease only if token still owns it (CAS).
func (p *Postgres) ReleasePollLease(ctx context.Context, invoiceID, token string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM poll_leases WHERE invoice_id = $1 AND lease_token = $2`, invoiceID, token)
	return err
}

// RecordNegativeCheck stamps the invoice's last definitive negative on-chain check.
func (p *Postgres) RecordNegativeCheck(ctx context.Context, invoiceID string, checkAt time.Time) error {
	_, err := p.pool.Exec(ctx, `UPDATE invoices SET last_negative_check_at = $2 WHERE id = $1 AND status = 'pending'`, invoiceID, checkAt)
	return err
}

// UpsertSchedule inserts or replaces a subscription's expiry schedule.
func (p *Postgres) UpsertSchedule(ctx context.Context, s model.Schedule) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO schedules (sub_id, anon_user_id, status, expires_at, grace_until)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (sub_id) DO UPDATE SET
			anon_user_id = EXCLUDED.anon_user_id,
			status       = EXCLUDED.status,
			expires_at   = EXCLUDED.expires_at,
			grace_until  = EXCLUDED.grace_until`,
		s.SubID, s.AnonUserID, s.Status, s.ExpiresAt, s.GraceUntil)
	return err
}

// GetSchedule loads a schedule by subscription id, ErrNotFound on a miss.
func (p *Postgres) GetSchedule(ctx context.Context, subID string) (model.Schedule, error) {
	row := p.pool.QueryRow(ctx, `
		SELECT sub_id, anon_user_id, status, expires_at, grace_until
		FROM schedules WHERE sub_id = $1`, subID)
	return scanSchedule(row)
}

// DueSchedules returns non-expired schedules whose expiry time has passed.
func (p *Postgres) DueSchedules(ctx context.Context, now time.Time) ([]model.Schedule, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT sub_id, anon_user_id, status, expires_at, grace_until
		FROM schedules WHERE status <> $1 AND expires_at <= $2`,
		"expired", now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// scanner is implemented by both pgx.Row and pgx.Rows, so scanning is shared.
type scanner interface {
	Scan(dest ...any) error
}

func scanInvoice(s scanner) (model.Invoice, error) {
	var inv model.Invoice
	var status string
	err := s.Scan(
		&inv.ID, &inv.Provider, &inv.AnonUserID, &inv.Plan, &inv.Currency, &inv.Amount,
		&inv.PayAddress, &inv.ProviderInvoiceID, &status, &inv.CreatedAt, &inv.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Invoice{}, ErrNotFound
	}
	if err != nil {
		return model.Invoice{}, err
	}
	inv.Status = model.Status(status)
	return inv, nil
}

func scanSchedule(s scanner) (model.Schedule, error) {
	var sc model.Schedule
	err := s.Scan(&sc.SubID, &sc.AnonUserID, &sc.Status, &sc.ExpiresAt, &sc.GraceUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Schedule{}, ErrNotFound
	}
	if err != nil {
		return model.Schedule{}, err
	}
	return sc, nil
}

package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/billing/internal/model"
)

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

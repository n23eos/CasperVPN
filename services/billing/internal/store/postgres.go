package store

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

type lockedConnKey struct{}

type postgresExecutor interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
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
// Repository calls made with fn's context use this same connection. This is needed
// for both correctness and liveness: a same-user waiter may occupy every other pool
// slot while blocked on the advisory lock.
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
	return fn(context.WithValue(ctx, lockedConnKey{}, conn))
}

func (p *Postgres) executor(ctx context.Context) postgresExecutor {
	if conn, ok := ctx.Value(lockedConnKey{}).(*pgxpool.Conn); ok {
		return conn
	}
	return p.pool
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

func (p *Postgres) ReserveInvoiceIntent(ctx context.Context, intent model.InvoiceIntent) (model.InvoiceIntent, bool, error) {
	tag, err := p.pool.Exec(ctx, `
		INSERT INTO invoice_intents (
			idempotency_key, request_hash, order_id, provider, anon_user_id,
			plan, currency, amount, state, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'reserved',$9)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		intent.IdempotencyKey, intent.RequestHash, intent.OrderID, intent.Provider,
		intent.AnonUserID, intent.Plan, intent.Currency, intent.Amount, intent.CreatedAt)
	if err != nil {
		return model.InvoiceIntent{}, false, err
	}
	got, err := p.GetInvoiceIntent(ctx, intent.IdempotencyKey)
	if err != nil {
		return model.InvoiceIntent{}, false, err
	}
	if got.RequestHash != intent.RequestHash {
		return model.InvoiceIntent{}, false, ErrConflict
	}
	return got, tag.RowsAffected() == 1, nil
}

func (p *Postgres) BeginInvoiceCreate(ctx context.Context, key string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE invoice_intents SET state = 'creating'
		WHERE idempotency_key = $1 AND state = 'reserved'`, key)
	return tag.RowsAffected() == 1, err
}

func (p *Postgres) CompleteInvoiceCreate(ctx context.Context, key string, inv model.Invoice) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var orderID, provider string
	if err := tx.QueryRow(ctx, `SELECT order_id, provider FROM invoice_intents WHERE idempotency_key=$1 FOR UPDATE`, key).Scan(&orderID, &provider); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if orderID != inv.ID || provider != inv.Provider {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invoices (
			id, provider, anon_user_id, plan, currency, amount,
			pay_address, provider_invoice_id, status, created_at, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (id) DO NOTHING`,
		inv.ID, inv.Provider, inv.AnonUserID, inv.Plan, inv.Currency, inv.Amount,
		inv.PayAddress, inv.ProviderInvoiceID, string(inv.Status), inv.CreatedAt, inv.ExpiresAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE invoice_intents SET state='ready' WHERE idempotency_key=$1`, key); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) FailInvoiceCreate(ctx context.Context, key string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE invoice_intents SET state='failed' WHERE idempotency_key=$1`, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) GetInvoiceIntent(ctx context.Context, key string) (model.InvoiceIntent, error) {
	var intent model.InvoiceIntent
	err := p.pool.QueryRow(ctx, `
		SELECT idempotency_key, request_hash, order_id, provider, anon_user_id,
		       plan, currency, amount, state, created_at
		FROM invoice_intents WHERE idempotency_key=$1`, key).Scan(
		&intent.IdempotencyKey, &intent.RequestHash, &intent.OrderID, &intent.Provider,
		&intent.AnonUserID, &intent.Plan, &intent.Currency, &intent.Amount,
		&intent.State, &intent.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.InvoiceIntent{}, ErrNotFound
	}
	return intent, err
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

// ClearNegativeCheck wipes the negative marker once the chain shows the invoice paid.
func (p *Postgres) ClearNegativeCheck(ctx context.Context, invoiceID string) error {
	_, err := p.pool.Exec(ctx, `UPDATE invoices SET last_negative_check_at = NULL WHERE id = $1 AND status = 'pending'`, invoiceID)
	return err
}

func (p *Postgres) StageInvoiceCredit(ctx context.Context, invoiceID, subID, anonUserID string, now time.Time, duration, grace time.Duration) (model.BillingDelivery, error) {
	tx, err := p.executor(ctx).BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.BillingDelivery{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if existing, err := getDeliveryByInvoice(ctx, tx, invoiceID); err == nil {
		return existing, tx.Commit(ctx)
	} else if !errors.Is(err, ErrNotFound) {
		return model.BillingDelivery{}, err
	}
	var invoiceUser, invoicePlan string
	if err := tx.QueryRow(ctx, `SELECT anon_user_id, plan FROM invoices WHERE id=$1 FOR UPDATE`, invoiceID).Scan(&invoiceUser, &invoicePlan); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.BillingDelivery{}, ErrNotFound
		}
		return model.BillingDelivery{}, err
	}
	if invoiceUser != anonUserID {
		return model.BillingDelivery{}, ErrConflict
	}
	sched, err := getScheduleForUpdate(ctx, tx, subID)
	base, revision := now, int64(1)
	if err == nil {
		revision = sched.Revision + 1
		if sched.ExpiresAt.After(base) {
			base = sched.ExpiresAt
		}
	} else if !errors.Is(err, ErrNotFound) {
		return model.BillingDelivery{}, err
	}
	expiresAt := base.Add(duration)
	delivery := model.BillingDelivery{
		ID: "invoice:" + invoiceID, InvoiceID: invoiceID, SubID: subID,
		AnonUserID: anonUserID, Revision: revision, Plan: invoicePlan, Status: "active",
		ExpiresAt: expiresAt, GraceUntil: expiresAt.Add(grace), CreatedAt: now,
	}
	if err := insertDelivery(ctx, tx, delivery); err != nil {
		return model.BillingDelivery{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO schedules (sub_id, anon_user_id, revision, plan, status, expires_at, grace_until)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (sub_id) DO UPDATE SET
			anon_user_id=EXCLUDED.anon_user_id, revision=EXCLUDED.revision,
			plan=EXCLUDED.plan, status=EXCLUDED.status, expires_at=EXCLUDED.expires_at,
			grace_until=EXCLUDED.grace_until`,
		delivery.SubID, delivery.AnonUserID, delivery.Revision, delivery.Plan, delivery.Status,
		delivery.ExpiresAt, delivery.GraceUntil); err != nil {
		return model.BillingDelivery{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.BillingDelivery{}, err
	}
	return delivery, nil
}

func (p *Postgres) StageScheduleTransition(ctx context.Context, subID string, expectedRevision int64, status string, now time.Time) (model.BillingDelivery, bool, error) {
	tx, err := p.executor(ctx).BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.BillingDelivery{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	sched, err := getScheduleForUpdate(ctx, tx, subID)
	if err != nil {
		return model.BillingDelivery{}, false, err
	}
	if sched.Revision != expectedRevision {
		return model.BillingDelivery{}, false, tx.Commit(ctx)
	}
	delivery := model.BillingDelivery{
		ID:    "schedule:" + subID + ":" + strconv.FormatInt(sched.Revision+1, 10),
		SubID: subID, AnonUserID: sched.AnonUserID, Revision: sched.Revision + 1,
		Plan: sched.Plan, Status: status,
		ExpiresAt: sched.ExpiresAt, GraceUntil: sched.GraceUntil, CreatedAt: now,
	}
	if err := insertDelivery(ctx, tx, delivery); err != nil {
		return model.BillingDelivery{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE schedules SET revision=$2, status=$3 WHERE sub_id=$1`, subID, delivery.Revision, status); err != nil {
		return model.BillingDelivery{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.BillingDelivery{}, false, err
	}
	return delivery, true, nil
}

func (p *Postgres) LeaseBillingDeliveries(ctx context.Context, olderThan time.Time, leaseFor time.Duration, limit int) ([]model.BillingDelivery, error) {
	rows, err := p.pool.Query(ctx, `
		UPDATE billing_deliveries SET leased_until=now()+make_interval(secs=>$2)
		WHERE id IN (
			SELECT id FROM billing_deliveries
			WHERE delivered_at IS NULL AND created_at < $1
			  AND (leased_until IS NULL OR leased_until <= now())
			ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT $3
		)
		RETURNING id, COALESCE(invoice_id,''), sub_id, anon_user_id, revision,
		          plan, status, expires_at, grace_until, created_at, COALESCE(delivered_at, 'epoch')`,
		olderThan, leaseFor.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.BillingDelivery
	for rows.Next() {
		delivery, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, delivery)
	}
	return out, rows.Err()
}

func (p *Postgres) CompleteBillingDelivery(ctx context.Context, deliveryID string) error {
	tx, err := p.executor(ctx).BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var invoiceID *string
	err = tx.QueryRow(ctx, `
		UPDATE billing_deliveries SET delivered_at=COALESCE(delivered_at,now()), leased_until=NULL
		WHERE id=$1 RETURNING invoice_id`, deliveryID).Scan(&invoiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if invoiceID != nil {
		if _, err := tx.Exec(ctx, `UPDATE invoices SET status='settled' WHERE id=$1`, *invoiceID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func insertDelivery(ctx context.Context, tx pgx.Tx, d model.BillingDelivery) error {
	var invoiceID any
	if d.InvoiceID != "" {
		invoiceID = d.InvoiceID
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO billing_deliveries (
			id, invoice_id, sub_id, anon_user_id, revision, plan, status,
			expires_at, grace_until, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		d.ID, invoiceID, d.SubID, d.AnonUserID, d.Revision, d.Plan, d.Status,
		d.ExpiresAt, d.GraceUntil, d.CreatedAt)
	return err
}

func getDeliveryByInvoice(ctx context.Context, tx pgx.Tx, invoiceID string) (model.BillingDelivery, error) {
	return scanDelivery(tx.QueryRow(ctx, `
		SELECT id, COALESCE(invoice_id,''), sub_id, anon_user_id, revision,
		       plan, status, expires_at, grace_until, created_at, COALESCE(delivered_at, 'epoch')
		FROM billing_deliveries WHERE invoice_id=$1`, invoiceID))
}

func getScheduleForUpdate(ctx context.Context, tx pgx.Tx, subID string) (model.Schedule, error) {
	return scanSchedule(tx.QueryRow(ctx, `
		SELECT sub_id, anon_user_id, revision, plan, status, expires_at, grace_until
		FROM schedules WHERE sub_id=$1 FOR UPDATE`, subID))
}

// UpsertSchedule inserts or replaces a subscription's expiry schedule.
func (p *Postgres) UpsertSchedule(ctx context.Context, s model.Schedule) error {
	_, err := p.executor(ctx).Exec(ctx, `
		INSERT INTO schedules (sub_id, anon_user_id, revision, plan, status, expires_at, grace_until)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (sub_id) DO UPDATE SET
			anon_user_id = EXCLUDED.anon_user_id,
			revision     = GREATEST(schedules.revision, EXCLUDED.revision),
			plan         = CASE WHEN EXCLUDED.plan <> '' THEN EXCLUDED.plan ELSE schedules.plan END,
			status       = EXCLUDED.status,
			expires_at   = EXCLUDED.expires_at,
			grace_until  = EXCLUDED.grace_until`,
		s.SubID, s.AnonUserID, s.Revision, s.Plan, s.Status, s.ExpiresAt, s.GraceUntil)
	return err
}

// GetSchedule loads a schedule by subscription id, ErrNotFound on a miss.
func (p *Postgres) GetSchedule(ctx context.Context, subID string) (model.Schedule, error) {
	row := p.executor(ctx).QueryRow(ctx, `
		SELECT sub_id, anon_user_id, revision, plan, status, expires_at, grace_until
		FROM schedules WHERE sub_id = $1`, subID)
	return scanSchedule(row)
}

// DueSchedules returns non-expired schedules whose expiry time has passed.
func (p *Postgres) DueSchedules(ctx context.Context, now time.Time) ([]model.Schedule, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT sub_id, anon_user_id, revision, plan, status, expires_at, grace_until
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
	err := s.Scan(&sc.SubID, &sc.AnonUserID, &sc.Revision, &sc.Plan, &sc.Status, &sc.ExpiresAt, &sc.GraceUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Schedule{}, ErrNotFound
	}
	if err != nil {
		return model.Schedule{}, err
	}
	return sc, nil
}

func scanDelivery(s scanner) (model.BillingDelivery, error) {
	var d model.BillingDelivery
	err := s.Scan(&d.ID, &d.InvoiceID, &d.SubID, &d.AnonUserID, &d.Revision, &d.Plan,
		&d.Status, &d.ExpiresAt, &d.GraceUntil, &d.CreatedAt, &d.DeliveredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.BillingDelivery{}, ErrNotFound
	}
	return d, err
}

func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

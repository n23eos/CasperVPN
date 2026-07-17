// Package store is billing's persistence seam. The in-memory implementation keeps
// the MVP self-contained and offline; a Postgres implementation drops in behind
// the same interface (see docs/billing.md — production gap).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/caspervpn/billing/internal/model"
)

// ErrNotFound is returned when a lookup misses.
var ErrNotFound = errors.New("store: not found")

// StuckSettlement identifies an invoice that was claimed for crediting but whose
// settlement never completed (the process died between the claim commit and the
// remote activation / status flip). Activated reports whether the remote activation
// was already applied for this invoice — if true, recovery only needs to flip the
// invoice to settled, NOT re-activate (re-activation would extend the period twice).
type StuckSettlement struct {
	InvoiceID string
	Activated bool
}

// Repository is the storage contract for billing. The idempotency guarantees live
// in two places:
//
//   - SeenEvent/RecordEvent dedup a webhook DELIVERY (replay of the same signed
//     delivery is a no-op).
//   - ClaimSettlement/ReleaseSettlement guarantee an invoice is credited AT MOST
//     ONCE across any number of distinct deliveries or polls. ClaimSettlement is
//     the atomic "I will credit this invoice" latch; ReleaseSettlement backs it out
//     if crediting fails, so a later retry can succeed.
type Repository interface {
	CreateInvoice(ctx context.Context, inv model.Invoice) error
	GetInvoice(ctx context.Context, id string) (model.Invoice, error)
	SetInvoiceStatus(ctx context.Context, id string, s model.Status) error
	OpenInvoices(ctx context.Context) ([]model.Invoice, error)

	// SeenEvent reports whether a (provider, externalID) delivery was already
	// processed to completion. RecordEvent marks it processed.
	SeenEvent(ctx context.Context, provider, externalID string) (bool, error)
	RecordEvent(ctx context.Context, provider, externalID string) error

	// WithUserLock runs fn while holding a per-user exclusive lock, serializing the
	// WHOLE activation of one user across instances so two distinct settlements for
	// the same user apply their periods one after another instead of racing (lost
	// update). Postgres uses a session-scoped advisory lock on a dedicated connection,
	// guaranteed released (and the connection destroyed rather than returned to the
	// pool if unlock ever fails; a crashed process drops the connection, which frees
	// the lock). The memory backend uses a process-local mutex and is explicitly NOT
	// cross-instance safe — horizontal production billing requires Postgres.
	WithUserLock(ctx context.Context, userID string, fn func(ctx context.Context) error) error

	// ClaimSettlement atomically claims the right to credit invoiceID. It returns
	// true only for the first caller; every later caller gets false until released.
	// The claim is stamped with a creation time so a crashed-mid-settle claim can be
	// recovered once it is older than the recovery threshold.
	ClaimSettlement(ctx context.Context, invoiceID string) (bool, error)
	// ReleaseSettlement backs out a claim (used when crediting failed).
	ReleaseSettlement(ctx context.Context, invoiceID string) error

	// MarkSettlementActivated records that the remote activation for invoiceID has
	// been applied. Idempotent. Recovery uses this to finish a crashed settlement
	// without re-activating (which would add a second subscription period).
	MarkSettlementActivated(ctx context.Context, invoiceID string) error
	// LeaseStuckSettlements atomically leases up to limit claimed-but-unsettled
	// invoices whose claim is older than olderThan and not currently leased, marking
	// each leased for leaseFor. Cross-process safe (FOR UPDATE SKIP LOCKED), so two
	// reconcilers never process the same invoice at once; a crashed lease expires and
	// is retaken. Returns the leased rows with their activation state.
	LeaseStuckSettlements(ctx context.Context, olderThan time.Time, leaseFor time.Duration, limit int) ([]StuckSettlement, error)
	// ExpireOverdue expires pending invoices whose effective deadline has STRICTLY
	// passed (now > deadline). For providers in onchainProviders the deadline is
	// expires_at + grace AND expiry additionally requires a durable negative check
	// taken at/after that deadline (last_negative_check_at >= deadline); every other
	// provider expires at expires_at with no grace/check. An invoice is never expired
	// while it carries a settlement claim OR a live poll lease. This is the sweep side
	// of the "confirmed-before-deadline+grace" policy (variant A) — not proof of
	// broadcast time.
	ExpireOverdue(ctx context.Context, now time.Time, onchainProviders []string, grace time.Duration) error

	// AcquirePollLease mints a fresh opaque lease token and takes the per-invoice
	// poll lease for owner until now+leaseFor, reclaiming an existing lease only if it
	// has lapsed (lease_until <= now). Returns the token and whether it was acquired.
	AcquirePollLease(ctx context.Context, invoiceID, owner string, leaseFor time.Duration) (token string, acquired bool, err error)
	// RenewPollLease extends the lease to now+leaseFor only if token still owns it
	// (CAS on invoice_id+lease_token). Returns false if ownership was lost.
	RenewPollLease(ctx context.Context, invoiceID, token string, leaseFor time.Duration) (bool, error)
	// ReleasePollLease drops the lease only if token still owns it (CAS), so a stale
	// owner can never release a lease reclaimed by someone else.
	ReleasePollLease(ctx context.Context, invoiceID, token string) error
	// RecordNegativeCheck stamps last_negative_check_at for a DEFINITIVE negative
	// on-chain check (payment absent/insufficient with the chain reachable) — never
	// for a chain API error.
	RecordNegativeCheck(ctx context.Context, invoiceID string, checkAt time.Time) error

	// Expiry schedule index (billing-owned; no PII).
	UpsertSchedule(ctx context.Context, s model.Schedule) error
	GetSchedule(ctx context.Context, subID string) (model.Schedule, error)
	DueSchedules(ctx context.Context, now time.Time) ([]model.Schedule, error)
}

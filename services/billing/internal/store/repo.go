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
	// ExpireOverdue transitions pending invoices past now to expired in one atomic
	// statement, EXCLUDING any invoice that carries a settlement claim — a paid
	// invoice still being recovered must never be buried as expired.
	ExpireOverdue(ctx context.Context, now time.Time) error

	// Expiry schedule index (billing-owned; no PII).
	UpsertSchedule(ctx context.Context, s model.Schedule) error
	GetSchedule(ctx context.Context, subID string) (model.Schedule, error)
	DueSchedules(ctx context.Context, now time.Time) ([]model.Schedule, error)
}

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
	ClaimSettlement(ctx context.Context, invoiceID string) (bool, error)
	// ReleaseSettlement backs out a claim (used when crediting failed).
	ReleaseSettlement(ctx context.Context, invoiceID string) error

	// Expiry schedule index (billing-owned; no PII).
	UpsertSchedule(ctx context.Context, s model.Schedule) error
	GetSchedule(ctx context.Context, subID string) (model.Schedule, error)
	DueSchedules(ctx context.Context, now time.Time) ([]model.Schedule, error)
}

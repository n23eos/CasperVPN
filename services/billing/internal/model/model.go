// Package model holds the dependency-free value types shared across the billing
// subsystem (invoices, payment events, expiry schedules). Keeping them in a leaf
// package avoids an import cycle between the store, the payment gateways and the
// processor. Plan/status are carried as plain strings here and converted to the
// frozen contracts enums at the service boundary.
package model

import "time"

// Status is the lifecycle of a payment/invoice as seen by billing. It is provider
// -agnostic: every gateway normalizes its own vocabulary into these values.
type Status string

const (
	StatusPending Status = "pending" // created, awaiting on-chain confirmation
	StatusSettled Status = "settled" // confirmed and credited
	StatusExpired Status = "expired" // window elapsed without sufficient payment
	StatusInvalid Status = "invalid" // provider marked it invalid (e.g. underpaid, double-spend)
)

// Invoice is a single crypto payment request bound to an anonymous account. It
// carries NO PII: AnonUserID is an opaque account id, never an email or phone.
type Invoice struct {
	ID                string    `json:"id"`                  // billing-side id
	Provider          string    `json:"provider"`            // gateway name that owns it
	AnonUserID        string    `json:"anon_user_id"`        // opaque account id (no PII)
	Plan              string    `json:"plan"`                // contracts.SubscriptionPlan value
	Currency          string    `json:"currency"`            // e.g. BTC, XMR, USDT_TRC20
	Amount            string    `json:"amount"`              // expected decimal amount
	PayAddress        string    `json:"pay_address"`         // crypto address OR hosted checkout link
	ProviderInvoiceID string    `json:"provider_invoice_id"` // id in the gateway
	Status            Status    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// Event is a normalized payment notification, produced from a webhook or a poll.
// ExternalID and InvoiceID drive the two-layer idempotency (see store).
type Event struct {
	Provider      string    `json:"provider"`
	ExternalID    string    `json:"external_id"` // provider delivery/event id — webhook-replay dedup key
	InvoiceID     string    `json:"invoice_id"`  // billing invoice id — settlement dedup key
	Status        Status    `json:"status"`
	Currency      string    `json:"currency"`
	Amount        string    `json:"amount"` // amount actually observed as paid
	Confirmations int       `json:"confirmations"`
	Timestamp     time.Time `json:"timestamp"`
}

// CreateInvoiceRequest is the semantic input to a gateway's CreateInvoice. Amount
// is resolved from the plan catalog before the gateway is called.
type CreateInvoiceRequest struct {
	AnonUserID string
	Plan       string
	Currency   string
	Amount     string
}

// Schedule is billing's own lightweight expiry index for a subscription. It does
// NOT duplicate the authoritative entitlement (control-plane owns that); it only
// records when billing must drive the next status transition. No PII.
type Schedule struct {
	SubID      string    `json:"sub_id"`
	AnonUserID string    `json:"anon_user_id"`
	Status     string    `json:"status"` // contracts.SubscriptionStatus value
	ExpiresAt  time.Time `json:"expires_at"`
	GraceUntil time.Time `json:"grace_until"`
}

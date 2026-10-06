package contracts

import "time"

// BillingPaymentStatus is the provider-independent invoice lifecycle.
type BillingPaymentStatus string

const (
	BillingPaymentPending BillingPaymentStatus = "pending"
	BillingPaymentSettled BillingPaymentStatus = "settled"
	BillingPaymentExpired BillingPaymentStatus = "expired"
	BillingPaymentInvalid BillingPaymentStatus = "invalid"
)

// BillingPlanOffer publishes configured prices and durations, without claiming
// that optional traffic, speed or device quotas are enforced on the data plane.
type BillingPlanOffer struct {
	ID              SubscriptionPlan  `json:"id"`
	DurationSeconds int64             `json:"duration_seconds"`
	GraceSeconds    int64             `json:"grace_seconds"`
	Prices          map[string]string `json:"prices"`
}

type BillingPlanOffers struct {
	Items []BillingPlanOffer `json:"items"`
}

// BillingInvoiceStatus omits provider identifiers and payment destinations.
// A settled invoice is not proof that the current account has usable access;
// delivery must also check the authoritative control-plane entitlement.
type BillingInvoiceStatus struct {
	InvoiceID string               `json:"invoice_id"`
	Plan      SubscriptionPlan     `json:"plan"`
	Status    BillingPaymentStatus `json:"status"`
	Amount    string               `json:"amount"`
	Currency  string               `json:"currency"`
	CreatedAt time.Time            `json:"created_at"`
	ExpiresAt time.Time            `json:"expires_at"`
}

type BillingInvoiceOverview struct {
	BillingInvoiceStatus
	AnonUserID string `json:"anon_user_id"`
}

type BillingInvoiceCounts struct {
	Pending int64 `json:"pending"`
	Settled int64 `json:"settled"`
	Expired int64 `json:"expired"`
	Invalid int64 `json:"invalid"`
}

type BillingOperatorSummary struct {
	Counts BillingInvoiceCounts     `json:"counts"`
	Recent []BillingInvoiceOverview `json:"recent"`
}

// Package payment defines the crypto gateway abstraction and the multi-provider
// registry. Providers are self-hosted (BTCPay-class) or direct on-chain — never a
// single custodial service that can be shut down. Several run at once; the failure
// of one does not stop sales.
package payment

import (
	"context"
	"errors"

	"github.com/caspervpn/billing/internal/model"
)

// ErrNoGateway means no registered gateway can serve the requested currency.
var ErrNoGateway = errors.New("payment: no gateway available for currency")

// ErrUnsupportedWebhook means the gateway is poll-based and has no webhook path.
var ErrUnsupportedWebhook = errors.New("payment: gateway does not accept webhooks")

// Gateway is one crypto payment path. Implementations MUST be independent of one
// another so the fleet stays diverse (anti-block: diversity over monoculture).
type Gateway interface {
	// Name is the stable provider identifier used in webhook routes and invoices.
	Name() string

	// SupportsCurrency reports whether this gateway can settle the given currency.
	SupportsCurrency(currency string) bool

	// CreateInvoice creates a payment request and returns a fully-populated invoice
	// (billing id, provider invoice id, pay address/checkout link, pending status).
	CreateInvoice(ctx context.Context, req model.CreateInvoiceRequest) (model.Invoice, error)

	// ParseWebhook verifies the signature over the raw body and normalizes the
	// payload into an Event. A bad signature MUST return an error and produce no
	// Event. Poll-based gateways return ErrUnsupportedWebhook.
	ParseWebhook(signature string, body []byte) (model.Event, error)

	// Poll checks the given open invoices against the chain/provider and returns any
	// settled/updated events. Webhook-driven gateways return nil.
	Poll(ctx context.Context, open []model.Invoice) ([]model.Event, error)
}

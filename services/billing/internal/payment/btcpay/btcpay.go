// Package btcpay is a self-hosted BTCPay-Server-class gateway adapter. BTCPay is
// open-source and operator-run, so there is no third-party custodian to seize —
// this is the censorship-resistant backbone rail from ADR-004. Invoices are
// created over the Greenfield API; settlement arrives via signed webhooks.
package btcpay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/caspervpn/billing/internal/idgen"
	"github.com/caspervpn/billing/internal/model"
)

const name = "btcpay"

// Config holds the operator's BTCPay connection. All values come from env/secret
// manager — never hardcoded.
type Config struct {
	BaseURL       string   // e.g. https://btcpay.example.org
	APIKey        string   // Greenfield API key
	StoreID       string   // BTCPay store id
	WebhookSecret string   // shared secret for webhook HMAC
	Currencies    []string // currencies this store settles
}

// ErrNoWebhookSecret means the gateway is configured (BaseURL set) but has no
// webhook secret. Without it the HMAC key is empty and any attacker can forge a
// "settled" webhook, so the gateway MUST NOT run in this state.
var ErrNoWebhookSecret = errors.New("btcpay: BTCPAY_WEBHOOK_SECRET must be set when BTCPAY_BASE_URL is configured")

// Validate reports a fatal misconfiguration: a configured gateway with an empty
// webhook secret. main calls this at startup so the service fails fast instead of
// silently accepting forged webhooks (empty HMAC key).
func (c Config) Validate() error {
	if c.BaseURL != "" && c.WebhookSecret == "" {
		return ErrNoWebhookSecret
	}
	return nil
}

// Gateway is the BTCPay adapter.
type Gateway struct {
	cfg        Config
	currencies map[string]bool
	http       *http.Client
	now        func() time.Time
}

// New builds a BTCPay gateway. The HTTP client carries a bounded timeout so a
// hung gateway cannot wedge an inbound request forever.
func New(cfg Config) *Gateway {
	set := make(map[string]bool, len(cfg.Currencies))
	for _, c := range cfg.Currencies {
		set[c] = true
	}
	return &Gateway{
		cfg:        cfg,
		currencies: set,
		http:       &http.Client{Timeout: 10 * time.Second},
		now:        time.Now,
	}
}

// Name implements payment.Gateway.
func (g *Gateway) Name() string { return name }

// SupportsCurrency implements payment.Gateway.
func (g *Gateway) SupportsCurrency(currency string) bool { return g.currencies[currency] }

type createReq struct {
	Amount   string            `json:"amount"`
	Currency string            `json:"currency"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type createResp struct {
	ID           string `json:"id"`
	CheckoutLink string `json:"checkoutLink"`
}

// CreateInvoice creates a BTCPay invoice. The anonymous account id is passed only
// as an opaque order reference — no PII crosses the boundary.
func (g *Gateway) CreateInvoice(ctx context.Context, req model.CreateInvoiceRequest) (model.Invoice, error) {
	if !g.currencies[req.Currency] {
		return model.Invoice{}, fmt.Errorf("btcpay: unsupported currency %q", req.Currency)
	}
	id := idgen.New()
	payload, err := json.Marshal(createReq{
		Amount:   req.Amount,
		Currency: req.Currency,
		Metadata: map[string]string{"orderId": id, "anonUserId": req.AnonUserID, "plan": req.Plan},
	})
	if err != nil {
		return model.Invoice{}, fmt.Errorf("btcpay: marshal: %w", err)
	}
	url := fmt.Sprintf("%s/api/v1/stores/%s/invoices", strings.TrimRight(g.cfg.BaseURL, "/"), g.cfg.StoreID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return model.Invoice{}, fmt.Errorf("btcpay: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "token "+g.cfg.APIKey)

	resp, err := g.http.Do(httpReq)
	if err != nil {
		return model.Invoice{}, fmt.Errorf("btcpay: create invoice: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return model.Invoice{}, fmt.Errorf("btcpay: create invoice status %d: %s", resp.StatusCode, string(b))
	}
	var cr createResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return model.Invoice{}, fmt.Errorf("btcpay: decode: %w", err)
	}
	now := g.now()
	return model.Invoice{
		ID:                id,
		Provider:          name,
		AnonUserID:        req.AnonUserID,
		Plan:              req.Plan,
		Currency:          req.Currency,
		Amount:            req.Amount,
		PayAddress:        cr.CheckoutLink, // BTCPay hosts the checkout; link stands in for an address
		ProviderInvoiceID: cr.ID,
		Status:            model.StatusPending,
		CreatedAt:         now,
		ExpiresAt:         now.Add(30 * time.Minute),
	}, nil
}

// webhookBody is the subset of BTCPay's webhook payload billing consumes.
type webhookBody struct {
	DeliveryID string `json:"deliveryId"`
	InvoiceID  string `json:"invoiceId"`
	Type       string `json:"type"`
	Timestamp  int64  `json:"timestamp"` // BTCPay delivery time, Unix seconds
	Metadata   struct {
		OrderID string `json:"orderId"`
	} `json:"metadata"`
}

// ParseWebhook verifies the "BTCPay-Sig: sha256=<hex>" header against the raw body
// and maps the event type into a normalized Event. ExternalID = deliveryId gives
// replay dedup; InvoiceID = the billing order id gives settlement dedup.
func (g *Gateway) ParseWebhook(signature string, body []byte) (model.Event, error) {
	// Defense in depth: an empty secret means the HMAC key is empty and every
	// signature is forgeable. Refuse to verify rather than accept forged events.
	if g.cfg.WebhookSecret == "" {
		return model.Event{}, ErrNoWebhookSecret
	}
	sig := strings.TrimPrefix(signature, "sha256=")
	mac := hmac.New(sha256.New, []byte(g.cfg.WebhookSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return model.Event{}, fmt.Errorf("btcpay: invalid webhook signature")
	}
	var wb webhookBody
	if err := json.Unmarshal(body, &wb); err != nil {
		return model.Event{}, fmt.Errorf("btcpay: decode webhook: %w", err)
	}
	status := mapType(wb.Type)
	if status == "" {
		// Not a settlement-relevant event (e.g. InvoiceCreated or a partial
		// InvoicePaymentSettled) — surface as pending, don't activate.
		status = model.StatusPending
	}
	// The billing invoice id is the orderId we set at creation time.
	invoiceID := wb.Metadata.OrderID
	return model.Event{
		Provider:      name,
		ExternalID:    wb.DeliveryID,
		InvoiceID:     invoiceID,
		Status:        status,
		Confirmations: 0,
		Timestamp:     g.eventTime(wb.Timestamp),
	}, nil
}

// eventTime prefers the provider's own delivery timestamp so the replay-window
// guard measures real event age; it falls back to now when BTCPay omits it.
func (g *Gateway) eventTime(unixSeconds int64) time.Time {
	if unixSeconds > 0 {
		return time.Unix(unixSeconds, 0).UTC()
	}
	return g.now()
}

func mapType(t string) model.Status {
	switch t {
	case "InvoiceSettled":
		// Only a fully settled invoice activates. InvoicePaymentSettled is a
		// single (possibly partial) payment and must NOT grant a full term.
		return model.StatusSettled
	case "InvoiceExpired":
		return model.StatusExpired
	case "InvoiceInvalid":
		return model.StatusInvalid
	default:
		return ""
	}
}

// Poll implements payment.Gateway; BTCPay is webhook-driven.
func (g *Gateway) Poll(context.Context, []model.Invoice) ([]model.Event, error) { return nil, nil }

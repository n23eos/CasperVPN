// Package mock is a controllable, self-hosted-style crypto gateway used for
// integration tests and local dev (a stand-in for a testnet gateway). It signs
// and verifies webhooks with an HMAC-SHA256 secret, exactly like a real gateway,
// so the full webhook path — signature check included — is exercised in tests.
package mock

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/caspervpn/billing/internal/idgen"
	"github.com/caspervpn/billing/internal/model"
)

const name = "mock"

// Gateway is the mock crypto gateway.
type Gateway struct {
	secret     []byte
	currencies map[string]bool
	ttl        time.Duration
	now        func() time.Time
}

// New builds a mock gateway that settles the given currencies.
func New(secret string, currencies []string) *Gateway {
	set := make(map[string]bool, len(currencies))
	for _, c := range currencies {
		set[c] = true
	}
	return &Gateway{
		secret:     []byte(secret),
		currencies: set,
		ttl:        30 * time.Minute,
		now:        time.Now,
	}
}

// Name implements payment.Gateway.
func (g *Gateway) Name() string { return name }

// SupportsCurrency implements payment.Gateway.
func (g *Gateway) SupportsCurrency(currency string) bool { return g.currencies[currency] }

// CreateInvoice implements payment.Gateway.
func (g *Gateway) CreateInvoice(_ context.Context, req model.CreateInvoiceRequest) (model.Invoice, error) {
	if !g.currencies[req.Currency] {
		return model.Invoice{}, fmt.Errorf("mock: unsupported currency %q", req.Currency)
	}
	id := idgen.New()
	now := g.now()
	return model.Invoice{
		ID:                id,
		Provider:          name,
		AnonUserID:        req.AnonUserID,
		Plan:              req.Plan,
		Currency:          req.Currency,
		Amount:            req.Amount,
		PayAddress:        "mock:" + id,
		ProviderInvoiceID: id,
		Status:            model.StatusPending,
		CreatedAt:         now,
		ExpiresAt:         now.Add(g.ttl),
	}, nil
}

// webhookPayload is the mock's on-the-wire webhook body.
type webhookPayload struct {
	ExternalID    string `json:"external_id"`
	InvoiceID     string `json:"invoice_id"`
	Status        string `json:"status"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Confirmations int    `json:"confirmations"`
	Timestamp     string `json:"ts"`
}

// Sign returns the hex HMAC-SHA256 signature for a body — a test helper mirroring
// what a real gateway computes before delivering a webhook.
func (g *Gateway) Sign(body []byte) string {
	mac := hmac.New(sha256.New, g.secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// ParseWebhook verifies the HMAC signature over body, then normalizes it.
func (g *Gateway) ParseWebhook(signature string, body []byte) (model.Event, error) {
	expected := g.Sign(body)
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return model.Event{}, fmt.Errorf("mock: invalid webhook signature")
	}
	var p webhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return model.Event{}, fmt.Errorf("mock: decode webhook: %w", err)
	}
	ts := g.now()
	if p.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, p.Timestamp); err == nil {
			ts = parsed
		}
	}
	return model.Event{
		Provider:      name,
		ExternalID:    p.ExternalID,
		InvoiceID:     p.InvoiceID,
		Status:        model.Status(p.Status),
		Currency:      p.Currency,
		Amount:        p.Amount,
		Confirmations: p.Confirmations,
		Timestamp:     ts,
	}, nil
}

// Poll implements payment.Gateway; the mock is webhook-driven.
func (g *Gateway) Poll(context.Context, []model.Invoice) ([]model.Event, error) { return nil, nil }

// Package onchain is a direct on-chain verification gateway: no gateway service at
// all, just watching addresses on a chain the operator queries themselves. This is
// the maximally-independent payment path — nothing to seize or shut down. It is
// poll-based: a fresh receive address per invoice, confirmed against a ChainClient.
package onchain

import (
	"context"
	"fmt"
	"time"

	"github.com/caspervpn/billing/internal/idgen"
	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/money"
	"github.com/caspervpn/billing/internal/payment"
)

const name = "onchain"

// ChainClient reads settlement state for a receive address. Concrete impls hit a
// node RPC or explorer; tests inject a fake. Kept behind an interface so no
// specific chain/provider is hardcoded.
type ChainClient interface {
	// AddressStatus returns the total received amount (decimal string) and the
	// number of confirmations on the funding transaction(s) for addr.
	AddressStatus(ctx context.Context, currency, addr string) (received string, confirmations int, err error)
}

// AddressPool hands out fresh receive addresses (e.g. derived from an HD wallet).
type AddressPool interface {
	Next(currency string) (string, error)
}

// Gateway is the direct on-chain gateway.
type Gateway struct {
	currencies map[string]bool
	minConf    int
	chain      ChainClient
	pool       AddressPool
	ttl        time.Duration
	now        func() time.Time
}

// New builds an on-chain gateway requiring minConf confirmations to settle.
func New(currencies []string, minConf int, chain ChainClient, pool AddressPool) *Gateway {
	set := make(map[string]bool, len(currencies))
	for _, c := range currencies {
		set[c] = true
	}
	return &Gateway{
		currencies: set,
		minConf:    minConf,
		chain:      chain,
		pool:       pool,
		ttl:        60 * time.Minute,
		now:        time.Now,
	}
}

// Name implements payment.Gateway.
func (g *Gateway) Name() string { return name }

// SupportsCurrency implements payment.Gateway.
func (g *Gateway) SupportsCurrency(currency string) bool { return g.currencies[currency] }

// CreateInvoice allocates a fresh receive address for the payment.
func (g *Gateway) CreateInvoice(_ context.Context, req model.CreateInvoiceRequest) (model.Invoice, error) {
	if !g.currencies[req.Currency] {
		return model.Invoice{}, fmt.Errorf("onchain: unsupported currency %q", req.Currency)
	}
	addr, err := g.pool.Next(req.Currency)
	if err != nil {
		return model.Invoice{}, fmt.Errorf("onchain: allocate address: %w", err)
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
		PayAddress:        addr,
		ProviderInvoiceID: addr,
		Status:            model.StatusPending,
		CreatedAt:         now,
		ExpiresAt:         now.Add(g.ttl),
	}, nil
}

// ParseWebhook implements payment.Gateway; on-chain has no webhook.
func (g *Gateway) ParseWebhook(string, []byte) (model.Event, error) {
	return model.Event{}, payment.ErrUnsupportedWebhook
}

// Poll checks each of this gateway's open invoices against the chain and emits a
// settled event once confirmations and amount both clear the thresholds. The
// event's ExternalID is stable per invoice, so a repeated poll of an already-paid
// address dedups at the store and never double-credits.
func (g *Gateway) Poll(ctx context.Context, open []model.Invoice) ([]model.Event, error) {
	var events []model.Event
	now := g.now()
	for _, inv := range open {
		if inv.Provider != name {
			continue
		}
		// Never settle an invoice whose payment window has already elapsed — a late
		// on-chain arrival must not silently activate an expired order.
		if !inv.ExpiresAt.IsZero() && now.After(inv.ExpiresAt) {
			continue
		}
		received, confs, err := g.chain.AddressStatus(ctx, inv.Currency, inv.PayAddress)
		if err != nil {
			continue // transient chain error; try again next poll
		}
		if confs < g.minConf {
			continue
		}
		enough, err := money.GTE(received, inv.Amount)
		if err != nil || !enough {
			continue
		}
		events = append(events, model.Event{
			Provider:      name,
			ExternalID:    "onchain:" + inv.ID,
			InvoiceID:     inv.ID,
			Status:        model.StatusSettled,
			Currency:      inv.Currency,
			Amount:        received,
			Confirmations: confs,
			Timestamp:     g.now(),
		})
	}
	return events, nil
}

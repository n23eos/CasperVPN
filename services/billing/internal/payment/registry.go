package payment

import (
	"context"
	"fmt"

	"github.com/caspervpn/billing/internal/model"
)

// Registry holds every active gateway. Invoice creation walks the gateways that
// support the currency and returns the first success — so one gateway being down
// (or a specific provider being seized) never blocks a sale as long as another can
// serve the currency. This is the anti-block "no single point of pressure" rule in
// the payment layer.
type Registry struct {
	order  []Gateway
	byName map[string]Gateway
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Gateway)}
}

// Register adds a gateway. Later registrations are tried after earlier ones.
func (r *Registry) Register(g Gateway) {
	r.order = append(r.order, g)
	r.byName[g.Name()] = g
}

// Get returns a gateway by name (used for webhook routing).
func (r *Registry) Get(name string) (Gateway, bool) {
	g, ok := r.byName[name]
	return g, ok
}

// Gateways returns all registered gateways (used by the poller).
func (r *Registry) Gateways() []Gateway {
	return r.order
}

// CreateInvoice tries each gateway that supports the currency, in registration
// order, until one succeeds. If a preferred provider is given it is tried first;
// if it fails, the others still get a chance (failover).
func (r *Registry) CreateInvoice(ctx context.Context, preferred string, req model.CreateInvoiceRequest) (model.Invoice, error) {
	var lastErr error
	tried := make(map[string]bool)

	try := func(g Gateway) (model.Invoice, bool) {
		if g == nil || tried[g.Name()] || !g.SupportsCurrency(req.Currency) {
			return model.Invoice{}, false
		}
		tried[g.Name()] = true
		inv, err := g.CreateInvoice(ctx, req)
		if err != nil {
			lastErr = err
			return model.Invoice{}, false
		}
		return inv, true
	}

	if preferred != "" {
		if g, ok := r.byName[preferred]; ok {
			if inv, ok := try(g); ok {
				return inv, nil
			}
		}
	}
	for _, g := range r.order {
		if inv, ok := try(g); ok {
			return inv, nil
		}
	}
	if lastErr != nil {
		return model.Invoice{}, fmt.Errorf("payment: all gateways failed for %s: %w", req.Currency, lastErr)
	}
	return model.Invoice{}, ErrNoGateway
}

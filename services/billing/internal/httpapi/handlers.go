// Package httpapi exposes the billing HTTP surface: invoice creation, gateway
// webhooks and a health probe. It carries no PII — accounts are referenced by an
// opaque anon_user_id only.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/payment"
	"github.com/caspervpn/billing/internal/plan"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/contracts"
)

// API bundles the billing HTTP handlers.
type API struct {
	registry       *payment.Registry
	processor      *payment.Processor
	store          store.Repository
	catalog        *plan.Catalog
	invoiceLimiter *stripedLimiter
}

// New builds the API.
func New(registry *payment.Registry, processor *payment.Processor, repo store.Repository, catalog *plan.Catalog) *API {
	return &API{
		registry:       registry,
		processor:      processor,
		store:          repo,
		catalog:        catalog,
		invoiceLimiter: newStripedLimiter(defaultInvoiceBurst, defaultInvoiceRefillRate, nil),
	}
}

// Routes returns the configured mux.
func (a *API) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.health)
	mux.HandleFunc("/v1/invoices", a.createInvoice)
	mux.HandleFunc("/v1/webhooks/", a.webhook)
	return mux
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "billing"})
}

type createInvoiceReq struct {
	AnonUserID string `json:"anon_user_id"`
	Plan       string `json:"plan"`
	Currency   string `json:"currency"`
	Provider   string `json:"provider,omitempty"` // optional preference
}

type createInvoiceResp struct {
	InvoiceID  string `json:"invoice_id"`
	Provider   string `json:"provider"`
	PayAddress string `json:"pay_address"`
	Amount     string `json:"amount"`
	Currency   string `json:"currency"`
	ExpiresAt  string `json:"expires_at"`
}

// createInvoice validates input, prices the plan from the catalog and asks the
// registry to open an invoice (with gateway failover).
func (a *API) createInvoice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req createInvoiceReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWebhookBody)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.AnonUserID == "" {
		writeErr(w, http.StatusBadRequest, "anon_user_id required")
		return
	}
	// Rate-limit invoice creation per account so one abuser can't flood the crypto
	// gateways (without throttling everyone via a single global bucket).
	if !a.invoiceLimiter.allow(req.AnonUserID) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	planID := contracts.SubscriptionPlan(req.Plan)
	if !planID.Valid() {
		writeErr(w, http.StatusBadRequest, "unknown plan")
		return
	}
	if req.Currency == "" {
		writeErr(w, http.StatusBadRequest, "currency required")
		return
	}
	amount, ok := a.catalog.Price(planID, req.Currency)
	if !ok {
		writeErr(w, http.StatusBadRequest, "no price for plan/currency")
		return
	}

	inv, err := a.registry.CreateInvoice(r.Context(), req.Provider, model.CreateInvoiceRequest{
		AnonUserID: req.AnonUserID,
		Plan:       req.Plan,
		Currency:   req.Currency,
		Amount:     amount,
	})
	if err != nil {
		if errors.Is(err, payment.ErrNoGateway) {
			writeErr(w, http.StatusServiceUnavailable, "no gateway for currency")
			return
		}
		writeErr(w, http.StatusBadGateway, "gateway error")
		return
	}
	if err := a.store.CreateInvoice(r.Context(), inv); err != nil {
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	writeJSON(w, http.StatusCreated, createInvoiceResp{
		InvoiceID:  inv.ID,
		Provider:   inv.Provider,
		PayAddress: inv.PayAddress,
		Amount:     inv.Amount,
		Currency:   inv.Currency,
		ExpiresAt:  inv.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

// webhook verifies and processes a gateway notification. Signature verification
// lives in the gateway; a bad signature yields 401 and no state change.
func (a *API) webhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	provider := strings.TrimPrefix(r.URL.Path, "/v1/webhooks/")
	if provider == "" {
		writeErr(w, http.StatusNotFound, "provider required")
		return
	}
	gw, ok := a.registry.Get(provider)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown provider")
		return
	}
	body, err := readLimitedBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	ev, err := gw.ParseWebhook(webhookSignature(r), body)
	if err != nil {
		// Bad signature or unparseable payload — reject, change nothing.
		writeErr(w, http.StatusUnauthorized, "invalid webhook")
		return
	}
	if err := a.processor.Process(r.Context(), ev); err != nil {
		// Terminal, non-retryable outcomes are acknowledged so the gateway stops
		// redelivering; transient failures return 5xx to trigger provider retry.
		if errors.Is(err, payment.ErrUnderpaid) || errors.Is(err, payment.ErrStaleEvent) ||
			errors.Is(err, payment.ErrProviderMismatch) || errors.Is(err, payment.ErrCurrencyMismatch) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
			return
		}
		writeErr(w, http.StatusBadGateway, "processing failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

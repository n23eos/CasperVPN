// Package httpapi exposes the billing HTTP surface: invoice creation, private
// reads, gateway webhooks and health probes. Accounts use opaque anon_user_id
// values rather than direct identifiers.
package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/caspervpn/platform/httpjson"

	"github.com/caspervpn/billing/internal/idgen"
	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/payment"
	"github.com/caspervpn/billing/internal/plan"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/contracts"
)

// API bundles the billing HTTP handlers.
type API struct {
	registry           *payment.Registry
	processor          *payment.Processor
	store              store.Repository
	catalog            *plan.Catalog
	invoiceLimiter     *stripedLimiter
	invoiceToken       string
	requireInvoiceAuth bool
}

type Config struct {
	InvoiceToken       string
	RequireInvoiceAuth bool
}

const privateReadTimeout = 2 * time.Second

// New builds the API.
func New(registry *payment.Registry, processor *payment.Processor, repo store.Repository, catalog *plan.Catalog) *API {
	return NewWithConfig(registry, processor, repo, catalog, Config{})
}

func NewWithConfig(registry *payment.Registry, processor *payment.Processor, repo store.Repository, catalog *plan.Catalog, cfg Config) *API {
	return &API{
		registry:           registry,
		processor:          processor,
		store:              repo,
		catalog:            catalog,
		invoiceLimiter:     newStripedLimiter(defaultInvoiceBurst, defaultInvoiceRefillRate, nil),
		invoiceToken:       cfg.InvoiceToken,
		requireInvoiceAuth: cfg.RequireInvoiceAuth,
	}
}

// Routes returns the configured mux.
func (a *API) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.health)
	mux.HandleFunc("/readyz", a.ready)
	mux.HandleFunc("/v1/invoices", a.createInvoice)
	mux.HandleFunc("/v1/plans", a.listPlans)
	mux.HandleFunc("/v1/accounts/", a.latestInvoice)
	mux.HandleFunc("/v1/operator/summary", a.operatorSummary)
	mux.HandleFunc("/v1/webhooks/", a.webhook)
	return mux
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		httpjson.Error(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]string{"status": "ready", "service": "billing"})
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	httpjson.Write(w, http.StatusOK, map[string]string{"status": "ok", "service": "billing"})
}

type createInvoiceReq struct {
	AnonUserID string `json:"anon_user_id"`
	Plan       string `json:"plan"`
	Currency   string `json:"currency"`
	Provider   string `json:"provider,omitempty"` // optional preference
}

type createInvoiceResp struct {
	InvoiceID   string `json:"invoice_id"`
	Provider    string `json:"provider"`
	PayAddress  string `json:"pay_address"`
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	ExpiresAt   string `json:"expires_at"`
	CheckoutURL string `json:"checkout_url"`
}

func (a *API) listPlans(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpjson.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !a.invoiceReadAuthorized(r) {
		httpjson.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	plans := a.catalog.List()
	offers := make([]contracts.BillingPlanOffer, 0, len(plans))
	for _, p := range plans {
		prices := make(map[string]string, len(p.Prices))
		for currency, amount := range p.Prices {
			if _, err := a.registry.Select("", currency); err == nil {
				prices[currency] = amount
			}
		}
		if len(prices) == 0 {
			continue
		}
		offers = append(offers, contracts.BillingPlanOffer{
			ID: p.ID, DurationSeconds: int64(p.Duration / time.Second),
			GraceSeconds: int64(p.Grace / time.Second), Prices: prices,
		})
	}
	httpjson.Write(w, http.StatusOK, contracts.BillingPlanOffers{Items: offers})
}

func (a *API) latestInvoice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpjson.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !a.invoiceReadAuthorized(r) {
		httpjson.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	anonUserID, ok := latestInvoiceAccount(r.URL.Path)
	if !ok {
		httpjson.Error(w, http.StatusNotFound, "not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), privateReadTimeout)
	defer cancel()
	inv, err := a.store.LatestInvoice(ctx, anonUserID)
	if errors.Is(err, store.ErrNotFound) {
		httpjson.Error(w, http.StatusNotFound, "invoice not found")
		return
	}
	if err != nil {
		httpjson.Error(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	httpjson.Write(w, http.StatusOK, invoiceStatus(inv))
}

func (a *API) operatorSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpjson.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !a.invoiceReadAuthorized(r) {
		httpjson.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), privateReadTimeout)
	defer cancel()
	counts, recent, err := a.store.InvoiceSummary(ctx, 50)
	if err != nil {
		httpjson.Error(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	overviews := make([]contracts.BillingInvoiceOverview, len(recent))
	for i, inv := range recent {
		overviews[i] = contracts.BillingInvoiceOverview{
			BillingInvoiceStatus: invoiceStatus(inv),
			AnonUserID:           inv.AnonUserID,
		}
	}
	httpjson.Write(w, http.StatusOK, contracts.BillingOperatorSummary{
		Counts: contracts.BillingInvoiceCounts{
			Pending: counts.Pending, Settled: counts.Settled,
			Expired: counts.Expired, Invalid: counts.Invalid,
		},
		Recent: overviews,
	})
}

func (a *API) invoiceReadAuthorized(r *http.Request) bool {
	return !a.requireInvoiceAuth || validBearer(r.Header.Get("Authorization"), a.invoiceToken)
}

func latestInvoiceAccount(path string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/v1/accounts/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "invoices" || parts[2] != "latest" {
		return "", false
	}
	return parts[0], true
}

func invoiceStatus(inv model.InvoiceOverview) contracts.BillingInvoiceStatus {
	return contracts.BillingInvoiceStatus{
		InvoiceID: inv.InvoiceID,
		Plan:      contracts.SubscriptionPlan(inv.Plan),
		Status:    contracts.BillingPaymentStatus(inv.Status),
		Amount:    inv.Amount,
		Currency:  inv.Currency,
		CreatedAt: inv.CreatedAt.UTC(),
		ExpiresAt: inv.ExpiresAt.UTC(),
	}
}

// createInvoice validates input, prices the plan from the catalog and asks the
// registry to open an invoice (with gateway failover).
func (a *API) createInvoice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpjson.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.requireInvoiceAuth && !validBearer(r.Header.Get("Authorization"), a.invoiceToken) {
		httpjson.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 200 {
		httpjson.Error(w, http.StatusBadRequest, "valid Idempotency-Key required")
		return
	}
	var req createInvoiceReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWebhookBody)).Decode(&req); err != nil {
		httpjson.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.AnonUserID == "" {
		httpjson.Error(w, http.StatusBadRequest, "anon_user_id required")
		return
	}
	planID := contracts.SubscriptionPlan(req.Plan)
	if !planID.Valid() {
		httpjson.Error(w, http.StatusBadRequest, "unknown plan")
		return
	}
	if req.Currency == "" {
		httpjson.Error(w, http.StatusBadRequest, "currency required")
		return
	}
	intent, err := a.store.GetInvoiceIntent(r.Context(), idempotencyKey)
	amount := ""
	if err == nil {
		amount = intent.Amount
	} else if errors.Is(err, store.ErrNotFound) {
		var priced bool
		amount, priced = a.catalog.Price(planID, req.Currency)
		if !priced {
			httpjson.Error(w, http.StatusBadRequest, "no price for plan/currency")
			return
		}
	} else {
		httpjson.Error(w, http.StatusInternalServerError, "store error")
		return
	}
	created := false
	if errors.Is(err, store.ErrNotFound) {
		gw, selectErr := a.registry.Select(req.Provider, req.Currency)
		if selectErr != nil {
			httpjson.Error(w, http.StatusServiceUnavailable, "no gateway for currency")
			return
		}
		requestHash := invoiceRequestHash(req, amount, gw.Name())
		if !a.invoiceLimiter.allow(req.AnonUserID) {
			httpjson.Error(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		intent, created, err = a.store.ReserveInvoiceIntent(r.Context(), model.InvoiceIntent{
			IdempotencyKey: idempotencyKey, RequestHash: requestHash, OrderID: idgen.New(),
			Provider: gw.Name(), AnonUserID: req.AnonUserID, Plan: req.Plan,
			Currency: req.Currency, Amount: amount, CreatedAt: time.Now().UTC(),
		})
	}
	requestHash := ""
	if err == nil {
		if req.Provider != "" && req.Provider != intent.Provider {
			httpjson.Error(w, http.StatusConflict, "Idempotency-Key already used with different parameters")
			return
		}
		requestHash = invoiceRequestHash(req, amount, intent.Provider)
	}
	if errors.Is(err, store.ErrConflict) || err == nil && intent.RequestHash != requestHash {
		httpjson.Error(w, http.StatusConflict, "Idempotency-Key already used with different parameters")
		return
	}
	if err != nil {
		httpjson.Error(w, http.StatusInternalServerError, "store error")
		return
	}
	inv, err := a.resolveInvoiceIntent(r, intent)
	if err != nil {
		httpjson.Error(w, http.StatusBadGateway, "gateway result unresolved")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpjson.Write(w, status, createInvoiceResp{
		InvoiceID:   inv.ID,
		Provider:    inv.Provider,
		PayAddress:  inv.PayAddress,
		Amount:      inv.Amount,
		Currency:    inv.Currency,
		ExpiresAt:   inv.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		CheckoutURL: inv.PayAddress,
	})
}

func invoiceRequestHash(req createInvoiceReq, amount, provider string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{
		req.AnonUserID, req.Plan, req.Currency, amount, provider,
	}, "\x00"))))
}

func (a *API) resolveInvoiceIntent(r *http.Request, intent model.InvoiceIntent) (model.Invoice, error) {
	if intent.State == "ready" {
		return a.store.GetInvoice(r.Context(), intent.OrderID)
	}
	if intent.State == "failed" {
		return model.Invoice{}, payment.ErrCreateRejected
	}
	gw, ok := a.registry.Get(intent.Provider)
	if !ok {
		return model.Invoice{}, payment.ErrNoGateway
	}
	req := model.CreateInvoiceRequest{
		OrderID: intent.OrderID, AnonUserID: intent.AnonUserID, Plan: intent.Plan,
		Currency: intent.Currency, Amount: intent.Amount,
	}
	if intent.State == "reserved" {
		began, err := a.store.BeginInvoiceCreate(r.Context(), intent.IdempotencyKey)
		if err != nil {
			return model.Invoice{}, err
		}
		if began {
			inv, err := gw.CreateInvoice(r.Context(), req)
			if err == nil {
				if err := a.store.CompleteInvoiceCreate(r.Context(), intent.IdempotencyKey, inv); err != nil {
					return model.Invoice{}, err
				}
				return inv, nil
			}
			if errors.Is(err, payment.ErrCreateRejected) {
				_ = a.store.FailInvoiceCreate(r.Context(), intent.IdempotencyKey)
				return model.Invoice{}, err
			}
		}
	}
	recoverable, ok := gw.(payment.RecoverableGateway)
	if !ok {
		return model.Invoice{}, payment.ErrCreateAmbiguous
	}
	inv, found, err := recoverable.LookupInvoice(r.Context(), req)
	if err != nil || !found {
		if err == nil {
			err = payment.ErrCreateAmbiguous
		}
		return model.Invoice{}, err
	}
	if err := a.store.CompleteInvoiceCreate(r.Context(), intent.IdempotencyKey, inv); err != nil {
		return model.Invoice{}, err
	}
	return inv, nil
}

func validBearer(header, token string) bool {
	const prefix = "Bearer "
	if token == "" || !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return len(got) == len(token) && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// webhook verifies and processes a gateway notification. Signature verification
// lives in the gateway; a bad signature yields 401 and no state change.
func (a *API) webhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpjson.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	provider := strings.TrimPrefix(r.URL.Path, "/v1/webhooks/")
	if provider == "" {
		httpjson.Error(w, http.StatusNotFound, "provider required")
		return
	}
	gw, ok := a.registry.Get(provider)
	if !ok {
		httpjson.Error(w, http.StatusNotFound, "unknown provider")
		return
	}
	body, err := readLimitedBody(r)
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, "read body")
		return
	}
	ev, err := gw.ParseWebhook(webhookSignature(r), body)
	if err != nil {
		// Bad signature or unparseable payload - reject, change nothing.
		httpjson.Error(w, http.StatusUnauthorized, "invalid webhook")
		return
	}
	if err := a.processor.Process(r.Context(), ev); err != nil {
		// Terminal, non-retryable outcomes are acknowledged so the gateway stops
		// redelivering; transient failures return 5xx to trigger provider retry.
		if errors.Is(err, payment.ErrUnderpaid) || errors.Is(err, payment.ErrStaleEvent) ||
			errors.Is(err, payment.ErrProviderMismatch) || errors.Is(err, payment.ErrCurrencyMismatch) {
			httpjson.Write(w, http.StatusOK, map[string]string{"status": "rejected"})
			return
		}
		httpjson.Error(w, http.StatusBadGateway, "processing failed")
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]string{"status": "ok"})
}

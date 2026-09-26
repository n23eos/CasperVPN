package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/controlplane"
	"github.com/caspervpn/billing/internal/httpapi"
	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/payment"
	"github.com/caspervpn/billing/internal/payment/mock"
	"github.com/caspervpn/billing/internal/plan"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/billing/internal/subscription"
	"github.com/caspervpn/contracts"
)

const day = 24 * time.Hour

type harness struct {
	server *httptest.Server
	gw     *mock.Gateway
	fake   *controlplane.Fake
	api    *httpapi.API
}

func newHarness(t *testing.T) *harness {
	return newHarnessWithConfig(t, httpapi.Config{})
}

func newHarnessWithConfig(t *testing.T, cfg httpapi.Config) *harness {
	t.Helper()
	fake := controlplane.NewFake()
	fake.AddUser(contracts.User{
		ID:             "acct-1",
		Status:         contracts.UserStatusActive,
		RealityShortID: "ab12",
		UUID:           "uuid-1",
	})
	catalog := plan.NewCatalog(plan.Plan{
		ID:       contracts.SubscriptionPlanBasic,
		Duration: 30 * day,
		Grace:    3 * day,
		Prices:   map[string]string{"BTC": "0.0001"},
	})
	repo := store.NewMemory()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	act := subscription.NewActivator(fake, catalog, repo, func() time.Time { return now })
	proc := payment.NewProcessor(repo, act, 0, func() time.Time { return now })

	gw := mock.New("webhook-secret", []string{"BTC"})
	reg := payment.NewRegistry()
	reg.Register(gw)

	api := httpapi.NewWithConfig(reg, proc, repo, catalog, cfg)
	return &harness{server: httptest.NewServer(api.Routes()), gw: gw, fake: fake, api: api}
}

func postInvoice(t *testing.T, url, key, token string, body []byte) (*http.Response, createInvoiceResponse) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/invoices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post invoice: %v", err)
	}
	var out createInvoiceResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	_ = resp.Body.Close()
	return resp, out
}

type createInvoiceResponse struct {
	InvoiceID   string `json:"invoice_id"`
	CheckoutURL string `json:"checkout_url"`
}

func (h *harness) close() { h.server.Close() }

func (h *harness) createInvoice(t *testing.T) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"anon_user_id": "acct-1",
		"plan":         "basic",
		"currency":     "BTC",
	})
	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/invoices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-"+t.Name())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invoice status = %d, want 201", resp.StatusCode)
	}
	var out struct {
		InvoiceID string `json:"invoice_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.InvoiceID == "" {
		t.Fatal("empty invoice id")
	}
	return out.InvoiceID
}

// postWebhook signs and delivers a settlement webhook, returning the status code.
func (h *harness) postWebhook(t *testing.T, externalID, invoiceID string) int {
	t.Helper()
	body := []byte(fmt.Sprintf(
		`{"external_id":%q,"invoice_id":%q,"status":"settled","amount":"0.0001","currency":"BTC","confirmations":3}`,
		externalID, invoiceID,
	))
	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/webhooks/mock", bytes.NewReader(body))
	req.Header.Set("X-Signature", h.gw.Sign(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// End-to-end: create invoice, settle it via a signed webhook, subscription activates.
func TestEndToEnd_InvoiceToActivation(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	invID := h.createInvoice(t)
	if code := h.postWebhook(t, "delivery-1", invID); code != http.StatusOK {
		t.Fatalf("webhook status = %d, want 200", code)
	}

	if h.fake.CreateCalls != 1 {
		t.Fatalf("subscription create calls = %d, want 1", h.fake.CreateCalls)
	}
	user, _ := h.fake.GetUser(context.Background(), "acct-1")
	if user.SubscriptionID == nil {
		t.Fatal("user has no subscription after payment")
	}
	sub, _ := h.fake.Subscription(*user.SubscriptionID)
	if sub.Status != contracts.SubscriptionStatusActive || sub.ExpiresAt == nil {
		t.Fatalf("subscription not active: %+v", sub)
	}
}

// The required guarantee: a double webhook (two distinct deliveries settling the
// same invoice) grants exactly one term, over the full HTTP path.
func TestEndToEnd_DoubleWebhookSingleTerm(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	invID := h.createInvoice(t)
	if code := h.postWebhook(t, "delivery-1", invID); code != http.StatusOK {
		t.Fatalf("first webhook status = %d, want 200", code)
	}
	if code := h.postWebhook(t, "delivery-2", invID); code != http.StatusOK {
		t.Fatalf("second webhook status = %d, want 200", code)
	}

	if h.fake.CreateCalls != 1 {
		t.Fatalf("create calls = %d, want 1 (no double credit)", h.fake.CreateCalls)
	}
	if h.fake.SetCalls != 1 {
		t.Fatalf("set-period calls = %d, want 1 (no double term)", h.fake.SetCalls)
	}
}

// C6: invoice creation is rate-limited. With a frozen-time bucket of capacity 1
// and no refill, the second POST /v1/invoices returns 429.
func TestCreateInvoice_RateLimited(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.api.SetInvoiceLimiterForTest(1, 0, func() time.Time { return now })

	requestNo := 0
	post := func() int {
		requestNo++
		body, _ := json.Marshal(map[string]string{
			"anon_user_id": "acct-1", "plan": "basic", "currency": "BTC",
		})
		req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/invoices", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", fmt.Sprintf("rate-%d", requestNo))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := post(); code != http.StatusCreated {
		t.Fatalf("first create status = %d, want 201", code)
	}
	if code := post(); code != http.StatusTooManyRequests {
		t.Fatalf("second create status = %d, want 429", code)
	}
}

func TestCreateInvoice_IdempotencyKeyReturnsSameInvoiceAndRejectsConflict(t *testing.T) {
	h := newHarness(t)
	defer h.close()
	body := []byte(`{"anon_user_id":"acct-1","plan":"basic","currency":"BTC"}`)
	first, firstBody := postInvoice(t, h.server.URL, "stable-key", "", body)
	second, secondBody := postInvoice(t, h.server.URL, "stable-key", "", body)
	if first.StatusCode != http.StatusCreated || second.StatusCode != http.StatusOK {
		t.Fatalf("statuses = %d,%d; want 201,200", first.StatusCode, second.StatusCode)
	}
	if firstBody.InvoiceID == "" || secondBody.InvoiceID != firstBody.InvoiceID {
		t.Fatalf("invoice ids = %q,%q; want same non-empty id", firstBody.InvoiceID, secondBody.InvoiceID)
	}
	if firstBody.CheckoutURL == "" || secondBody.CheckoutURL != firstBody.CheckoutURL {
		t.Fatalf("checkout urls = %q,%q; want stable", firstBody.CheckoutURL, secondBody.CheckoutURL)
	}
	conflictBody := []byte(`{"anon_user_id":"acct-2","plan":"basic","currency":"BTC"}`)
	conflict, _ := postInvoice(t, h.server.URL, "stable-key", "", conflictBody)
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("conflicting reuse status = %d, want 409", conflict.StatusCode)
	}
}

func TestCreateInvoice_ProductionBearerRequired(t *testing.T) {
	h := newHarnessWithConfig(t, httpapi.Config{InvoiceToken: "internal-secret", RequireInvoiceAuth: true})
	defer h.close()
	body := []byte(`{"anon_user_id":"acct-1","plan":"basic","currency":"BTC"}`)
	unauthorized, _ := postInvoice(t, h.server.URL, "auth-1", "", body)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing bearer status = %d, want 401", unauthorized.StatusCode)
	}
	authorized, out := postInvoice(t, h.server.URL, "auth-1", "internal-secret", body)
	if authorized.StatusCode != http.StatusCreated || out.InvoiceID == "" {
		t.Fatalf("authorized status/id = %d/%q, want 201/non-empty", authorized.StatusCode, out.InvoiceID)
	}
}

type ambiguousGateway struct {
	invoice     model.Invoice
	createCalls int
	lookupCalls int
	found       bool
}

func (g *ambiguousGateway) Name() string                          { return "ambiguous" }
func (g *ambiguousGateway) SupportsCurrency(currency string) bool { return currency == "BTC" }
func (g *ambiguousGateway) CreateInvoice(_ context.Context, req model.CreateInvoiceRequest) (model.Invoice, error) {
	g.createCalls++
	g.invoice = model.Invoice{
		ID: req.OrderID, Provider: g.Name(), AnonUserID: req.AnonUserID, Plan: req.Plan,
		Currency: req.Currency, Amount: req.Amount, PayAddress: "https://checkout/" + req.OrderID,
		ProviderInvoiceID: "provider-" + req.OrderID, Status: model.StatusPending,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC),
	}
	return model.Invoice{}, payment.ErrCreateAmbiguous
}
func (g *ambiguousGateway) LookupInvoice(_ context.Context, req model.CreateInvoiceRequest) (model.Invoice, bool, error) {
	g.lookupCalls++
	return g.invoice, g.found && g.invoice.ID == req.OrderID, nil
}
func (g *ambiguousGateway) ParseWebhook(string, []byte) (model.Event, error) {
	return model.Event{}, payment.ErrUnsupportedWebhook
}
func (g *ambiguousGateway) Poll(context.Context, []model.Invoice) ([]model.Event, error) {
	return nil, nil
}

func TestCreateInvoice_AmbiguousProviderResultRecoveredWithoutDuplicatePost(t *testing.T) {
	fake := controlplane.NewFake()
	catalog := plan.NewCatalog(plan.Plan{ID: contracts.SubscriptionPlanBasic, Duration: 30 * day, Grace: 3 * day, Prices: map[string]string{"BTC": "0.0001"}})
	repo := store.NewMemory()
	act := subscription.NewActivator(fake, catalog, repo, time.Now)
	proc := payment.NewProcessor(repo, act, 0, time.Now)
	gw := &ambiguousGateway{found: true}
	reg := payment.NewRegistry()
	reg.Register(gw)
	server := httptest.NewServer(httpapi.New(reg, proc, repo, catalog).Routes())
	defer server.Close()
	body := []byte(`{"anon_user_id":"acct-1","plan":"basic","currency":"BTC"}`)
	first, firstBody := postInvoice(t, server.URL, "ambiguous-key", "", body)
	second, secondBody := postInvoice(t, server.URL, "ambiguous-key", "", body)
	if first.StatusCode != http.StatusCreated || second.StatusCode != http.StatusOK {
		t.Fatalf("statuses = %d,%d; want 201,200", first.StatusCode, second.StatusCode)
	}
	if firstBody.InvoiceID == "" || secondBody.InvoiceID != firstBody.InvoiceID {
		t.Fatalf("invoice ids = %q,%q; want recovered stable invoice", firstBody.InvoiceID, secondBody.InvoiceID)
	}
	if gw.createCalls != 1 || gw.lookupCalls != 1 {
		t.Fatalf("provider calls create=%d lookup=%d, want 1/1", gw.createCalls, gw.lookupCalls)
	}
}

func TestCreateInvoice_UnresolvedAmbiguousResultFailsClosed(t *testing.T) {
	catalog := plan.NewCatalog(plan.Plan{ID: contracts.SubscriptionPlanBasic, Duration: 30 * day, Grace: 3 * day, Prices: map[string]string{"BTC": "0.0001"}})
	repo := store.NewMemory()
	gw := &ambiguousGateway{found: false}
	reg := payment.NewRegistry()
	reg.Register(gw)
	server := httptest.NewServer(httpapi.New(reg, nil, repo, catalog).Routes())
	defer server.Close()
	body := []byte(`{"anon_user_id":"acct-1","plan":"basic","currency":"BTC"}`)
	first, _ := postInvoice(t, server.URL, "unresolved-key", "", body)
	second, _ := postInvoice(t, server.URL, "unresolved-key", "", body)
	if first.StatusCode != http.StatusBadGateway || second.StatusCode != http.StatusBadGateway {
		t.Fatalf("statuses = %d,%d; want 502,502", first.StatusCode, second.StatusCode)
	}
	if gw.createCalls != 1 || gw.lookupCalls != 2 {
		t.Fatalf("provider calls create=%d lookup=%d, want one create and lookup-only retries", gw.createCalls, gw.lookupCalls)
	}
}

type unhealthyStore struct {
	store.Repository
}

func (unhealthyStore) Ping(context.Context) error { return fmt.Errorf("database unavailable") }

func TestReadyzChecksRepository(t *testing.T) {
	catalog := plan.NewCatalog()
	base := store.NewMemory()
	api := httpapi.New(payment.NewRegistry(), nil, unhealthyStore{Repository: base}, catalog)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want 503", rec.Code)
	}
}

// A forged/bad signature is rejected with 401 and changes no state.
func TestEndToEnd_BadSignatureRejected(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	invID := h.createInvoice(t)
	body := []byte(fmt.Sprintf(`{"external_id":"x","invoice_id":%q,"status":"settled","amount":"0.0001","currency":"BTC"}`, invID))
	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/webhooks/mock", bytes.NewReader(body))
	req.Header.Set("X-Signature", "not-a-valid-signature")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if h.fake.CreateCalls != 0 {
		t.Fatalf("bad signature must not activate; create calls = %d", h.fake.CreateCalls)
	}
}

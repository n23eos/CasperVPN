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
}

func newHarness(t *testing.T) *harness {
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

	api := httpapi.New(reg, proc, repo, catalog)
	return &harness{server: httptest.NewServer(api.Routes()), gw: gw, fake: fake}
}

func (h *harness) close() { h.server.Close() }

func (h *harness) createInvoice(t *testing.T) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"anon_user_id": "acct-1",
		"plan":         "basic",
		"currency":     "BTC",
	})
	resp, err := http.Post(h.server.URL+"/v1/invoices", "application/json", bytes.NewReader(body))
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

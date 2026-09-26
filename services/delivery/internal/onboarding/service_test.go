package onboarding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

type fakeBackends struct {
	t               *testing.T
	cp              *httptest.Server
	billing         *httptest.Server
	mu              sync.Mutex
	telegramIDs     []int64
	idempotencyKeys []string
	billingCalls    int
	linkCalls       int
	subscription    contracts.Subscription
}

func newFakeBackends(t *testing.T) *fakeBackends {
	t.Helper()
	now := time.Now().UTC()
	f := &fakeBackends{t: t, subscription: contracts.Subscription{
		ID: "sub-1", UserID: "user-1", Plan: contracts.SubscriptionPlanBasic,
		Status: contracts.SubscriptionStatusActive, StartsAt: now, ExpiresAt: timePointer(now.Add(time.Hour)),
	}}
	f.cp = httptest.NewServer(http.HandlerFunc(f.handleCP))
	f.billing = httptest.NewServer(http.HandlerFunc(f.handleBilling))
	t.Cleanup(f.cp.Close)
	t.Cleanup(f.billing.Close)
	return f
}

func timePointer(value time.Time) *time.Time { return &value }

func (f *fakeBackends) handleCP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer cp-token" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/users/ensure-telegram":
		var request contracts.EnsureTelegram
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			f.t.Errorf("decode ensure user: %v", err)
		}
		f.mu.Lock()
		f.telegramIDs = append(f.telegramIDs, request.TelegramID)
		f.mu.Unlock()
		user := contracts.User{ID: "user-1", TelegramID: &request.TelegramID, Status: contracts.UserStatusActive, SubscriptionID: stringPointer("sub-1")}
		writeJSON(w, http.StatusOK, user)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/subscriptions/sub-1":
		writeJSON(w, http.StatusOK, f.subscription)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/subscriptions/sub-1/delivery-link":
		f.mu.Lock()
		f.linkCalls++
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, contracts.DeliveryLink{Token: "stable-token", SubscriptionID: "sub-1"})
	default:
		http.NotFound(w, r)
	}
}

func stringPointer(value string) *string { return &value }

func (f *fakeBackends) handleBilling(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer billing-token" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var request struct {
		AnonUserID string `json:"anon_user_id"`
		Plan       string `json:"plan"`
		Currency   string `json:"currency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		f.t.Errorf("decode invoice: %v", err)
	}
	if request.AnonUserID != "user-1" || request.Plan != "basic" || request.Currency != "XMR" {
		f.t.Errorf("invoice request = %+v", request)
	}
	f.mu.Lock()
	f.billingCalls++
	call := f.billingCalls
	f.idempotencyKeys = append(f.idempotencyKeys, r.Header.Get("Idempotency-Key"))
	f.mu.Unlock()
	if call == 1 {
		http.Error(w, "temporary", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"invoice_id": "inv-1", "provider": "fake", "checkout_url": "https://pay.example/inv-1",
		"amount": "1.0", "currency": "XMR", "expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (f *fakeBackends) service() *Service {
	return New(f.cp.URL, "cp-token", f.billing.URL, "billing-token", "https://subscriptions.example", f.cp.Client(), time.Millisecond)
}

func TestCreateInvoiceRetriesWithStableIdempotencyKey(t *testing.T) {
	f := newFakeBackends(t)
	invoice, err := f.service().CreateInvoice(context.Background(), 42, 77, contracts.SubscriptionPlanBasic, "XMR")
	if err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}
	if invoice.ID != "inv-1" || invoice.CheckoutURL != "https://pay.example/inv-1" {
		t.Fatalf("invoice = %+v", invoice)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.billingCalls != 2 || len(f.idempotencyKeys) != 2 || f.idempotencyKeys[0] != "tg-update-77" || f.idempotencyKeys[1] != "tg-update-77" {
		t.Fatalf("calls=%d keys=%v", f.billingCalls, f.idempotencyKeys)
	}
	for _, telegramID := range f.telegramIDs {
		if telegramID != 42 {
			t.Fatalf("ensure telegram identity = %d", telegramID)
		}
	}
}

func TestSubscriptionLinkIsStableAndUsesGrace(t *testing.T) {
	f := newFakeBackends(t)
	now := time.Now().UTC()
	f.subscription.Status = contracts.SubscriptionStatusPastDue
	f.subscription.ExpiresAt = timePointer(now.Add(-time.Minute))
	f.subscription.GraceUntil = timePointer(now.Add(time.Hour))
	service := f.service()
	service.now = func() time.Time { return now }
	first, err := service.SubscriptionLink(context.Background(), 42)
	if err != nil {
		t.Fatalf("first link: %v", err)
	}
	second, err := service.SubscriptionLink(context.Background(), 42)
	if err != nil {
		t.Fatalf("second link: %v", err)
	}
	if first.SubscriptionURL != "https://subscriptions.example/sub/stable-token" || second != first {
		t.Fatalf("links = %+v, %+v", first, second)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.linkCalls != 2 {
		t.Fatalf("delivery link calls = %d", f.linkCalls)
	}
}

func TestSubscriptionLinkRejectsExpiredEntitlementBeforeDeliveryLink(t *testing.T) {
	f := newFakeBackends(t)
	now := time.Now().UTC()
	f.subscription.ExpiresAt = timePointer(now.Add(-time.Second))
	service := f.service()
	service.now = func() time.Time { return now }
	if _, err := service.SubscriptionLink(context.Background(), 42); err == nil {
		t.Fatal("expired subscription must not receive a link")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.linkCalls != 0 {
		t.Fatal("delivery-link must not be called for expired entitlement")
	}
}

func TestSubscriptionLinkRejectsMismatchedUser(t *testing.T) {
	f := newFakeBackends(t)
	f.subscription.UserID = "other-user"
	if _, err := f.service().SubscriptionLink(context.Background(), 42); err == nil {
		t.Fatal("subscription owned by another user must be rejected")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.linkCalls != 0 {
		t.Fatal("delivery-link must not be called for mismatched identity")
	}
}

func TestClientRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	}))
	defer server.Close()
	service := New(server.URL, "token", server.URL, "token", "https://subscriptions.example", server.Client(), time.Millisecond)
	if err := service.EnsureUser(context.Background(), 42); err == nil {
		t.Fatal("oversized response must fail")
	}
}

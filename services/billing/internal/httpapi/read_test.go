package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/httpapi"
	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/payment"
	"github.com/caspervpn/billing/internal/payment/mock"
	"github.com/caspervpn/billing/internal/plan"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/contracts"
)

func newReadServer(t *testing.T, repo store.Repository, cfg httpapi.Config) *httptest.Server {
	t.Helper()
	catalog := plan.NewCatalog(
		plan.Plan{
			ID: contracts.SubscriptionPlanUnlimited, Duration: 60 * day, Grace: 5 * day,
			Prices: map[string]string{"BTC": "0.0003"},
		},
		plan.Plan{
			ID: contracts.SubscriptionPlanBasic, Duration: 30 * day, Grace: 3 * day,
			Prices: map[string]string{"XMR": "0.05", "BTC": "0.0001"},
		},
	)
	registry := payment.NewRegistry()
	registry.Register(mock.New("secret", []string{"BTC"}))
	return httptest.NewServer(httpapi.NewWithConfig(registry, nil, repo, catalog, cfg).Routes())
}

func getRead(t *testing.T, url, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp, body
}

type readCountingStore struct {
	store.Repository
	latestCalls     int
	summaryCalls    int
	latestDeadline  bool
	summaryDeadline bool
}

func (s *readCountingStore) LatestInvoice(ctx context.Context, _ string) (model.InvoiceOverview, error) {
	s.latestCalls++
	_, s.latestDeadline = ctx.Deadline()
	return model.InvoiceOverview{}, store.ErrNotFound
}

func (s *readCountingStore) InvoiceSummary(ctx context.Context, _ int) (model.InvoiceCounts, []model.InvoiceOverview, error) {
	s.summaryCalls++
	_, s.summaryDeadline = ctx.Deadline()
	return model.InvoiceCounts{}, nil, nil
}

func TestPrivateReads_RequireBearerBeforeStoreAccess(t *testing.T) {
	repo := &readCountingStore{Repository: store.NewMemory()}
	server := newReadServer(t, repo, httpapi.Config{InvoiceToken: "read-secret", RequireInvoiceAuth: true})
	defer server.Close()

	for _, path := range []string{"/v1/plans", "/v1/accounts/acct-1/invoices/latest", "/v1/operator/summary"} {
		for _, token := range []string{"", "wrong-secret"} {
			resp, _ := getRead(t, server.URL+path, token)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("GET %s with token %q status = %d, want 401", path, token, resp.StatusCode)
			}
		}
	}
	if repo.latestCalls != 0 || repo.summaryCalls != 0 {
		t.Fatalf("unauthorized store calls latest=%d summary=%d, want 0/0", repo.latestCalls, repo.summaryCalls)
	}
	wantStatuses := map[string]int{
		"/v1/plans":                           http.StatusOK,
		"/v1/accounts/acct-1/invoices/latest": http.StatusNotFound,
		"/v1/operator/summary":                http.StatusOK,
	}
	for path, want := range wantStatuses {
		resp, _ := getRead(t, server.URL+path, "read-secret")
		if resp.StatusCode != want {
			t.Fatalf("authorized GET %s status = %d, want %d", path, resp.StatusCode, want)
		}
	}
	if repo.latestCalls != 1 || repo.summaryCalls != 1 {
		t.Fatalf("authorized store calls latest=%d summary=%d, want 1/1", repo.latestCalls, repo.summaryCalls)
	}
	if !repo.latestDeadline || !repo.summaryDeadline {
		t.Fatalf("read deadlines latest=%t summary=%t, want both true", repo.latestDeadline, repo.summaryDeadline)
	}
}

func TestListPlans_SortedCopiesAndOnlyServedCurrencies(t *testing.T) {
	server := newReadServer(t, store.NewMemory(), httpapi.Config{})
	defer server.Close()
	resp, body := getRead(t, server.URL+"/v1/plans", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	var got contracts.BillingPlanOffers
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 2 || got.Items[0].ID != contracts.SubscriptionPlanBasic || got.Items[1].ID != contracts.SubscriptionPlanUnlimited {
		t.Fatalf("offers = %+v, want sorted basic/unlimited", got.Items)
	}
	basic := got.Items[0]
	if basic.DurationSeconds != int64((30*day)/time.Second) || basic.GraceSeconds != int64((3*day)/time.Second) {
		t.Fatalf("basic durations = %d/%d", basic.DurationSeconds, basic.GraceSeconds)
	}
	if len(basic.Prices) != 1 || basic.Prices["BTC"] != "0.0001" {
		t.Fatalf("basic prices = %#v, want only served BTC", basic.Prices)
	}
}

func TestListPlans_EmptyCatalogReturnsEmptyItems(t *testing.T) {
	api := httpapi.NewWithConfig(payment.NewRegistry(), nil, store.NewMemory(), plan.NewCatalog(), httpapi.Config{})
	server := httptest.NewServer(api.Routes())
	defer server.Close()
	resp, body := getRead(t, server.URL+"/v1/plans", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", resp.StatusCode, body)
	}
	var got contracts.BillingPlanOffers
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("items = %#v, want non-nil empty list", got.Items)
	}
}

func TestLatestInvoice_FiltersAccountOrdersStablyAndOmitsSecrets(t *testing.T) {
	repo := store.NewMemory()
	created := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	seedReadInvoice(t, repo, model.Invoice{
		ID: "inv-a", AnonUserID: "acct-1", Plan: "basic", Status: model.StatusPending,
		Amount: "0.0001", Currency: "BTC", CreatedAt: created, ExpiresAt: created.Add(time.Hour),
		Provider: "secret-provider-a", ProviderInvoiceID: "secret-provider-id-a", PayAddress: "secret-address-a",
	})
	seedReadInvoice(t, repo, model.Invoice{
		ID: "inv-z", AnonUserID: "acct-1", Plan: "unlimited", Status: model.StatusSettled,
		Amount: "0.0003", Currency: "BTC", CreatedAt: created, ExpiresAt: created.Add(time.Hour),
		Provider: "secret-provider-z", ProviderInvoiceID: "secret-provider-id-z", PayAddress: "secret-address-z",
	})
	seedReadInvoice(t, repo, model.Invoice{
		ID: "other-newer", AnonUserID: "acct-2", Plan: "basic", Status: model.StatusPending,
		Amount: "9", Currency: "BTC", CreatedAt: created.Add(time.Hour), ExpiresAt: created.Add(2 * time.Hour),
	})
	server := newReadServer(t, repo, httpapi.Config{})
	defer server.Close()

	resp, body := getRead(t, server.URL+"/v1/accounts/acct-1/invoices/latest", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	var got contracts.BillingInvoiceStatus
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.InvoiceID != "inv-z" || got.Plan != contracts.SubscriptionPlanUnlimited || got.Status != contracts.BillingPaymentSettled {
		t.Fatalf("latest = %+v, want inv-z for acct-1", got)
	}
	assertReadSecretsAbsent(t, body, "acct-1", "secret-provider", "secret-provider-id", "secret-address", "pay_address", "checkout_url")

	missing, _ := getRead(t, server.URL+"/v1/accounts/missing/invoices/latest", "")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", missing.StatusCode)
	}
}

func TestOperatorSummary_CountsBoundsAndStableOrder(t *testing.T) {
	repo := store.NewMemory()
	created := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	statuses := []model.Status{model.StatusPending, model.StatusSettled, model.StatusExpired, model.StatusInvalid}
	for i := 0; i < 53; i++ {
		status := statuses[i%len(statuses)]
		id := fmt.Sprintf("inv-%02d", i)
		seedReadInvoice(t, repo, model.Invoice{
			ID: id, AnonUserID: fmt.Sprintf("acct-%02d", i), Plan: "basic", Status: status,
			Amount: "0.0001", Currency: "BTC", CreatedAt: created, ExpiresAt: created.Add(time.Hour),
			Provider: "provider-secret", ProviderInvoiceID: "provider-id-secret", PayAddress: "address-secret",
		})
	}
	server := newReadServer(t, repo, httpapi.Config{})
	defer server.Close()
	resp, body := getRead(t, server.URL+"/v1/operator/summary", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	var got contracts.BillingOperatorSummary
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Counts.Pending != 14 || got.Counts.Settled != 13 || got.Counts.Expired != 13 || got.Counts.Invalid != 13 {
		t.Fatalf("counts = %+v", got.Counts)
	}
	if len(got.Recent) != 50 || got.Recent[0].InvoiceID != "inv-52" || got.Recent[49].InvoiceID != "inv-03" {
		t.Fatalf("recent bounds/order len=%d first=%q last=%q", len(got.Recent), got.Recent[0].InvoiceID, got.Recent[len(got.Recent)-1].InvoiceID)
	}
	assertReadSecretsAbsent(t, body, "provider-secret", "provider-id-secret", "address-secret", "pay_address", "checkout_url")
}

func TestPrivateReads_InvalidMethodAndPath(t *testing.T) {
	server := newReadServer(t, store.NewMemory(), httpapi.Config{})
	defer server.Close()
	for _, path := range []string{"/v1/plans", "/v1/accounts/acct-1/invoices/latest", "/v1/operator/summary"} {
		req, _ := http.NewRequest(http.MethodPost, server.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want 405", path, resp.StatusCode)
		}
	}
	malformed, _ := getRead(t, server.URL+"/v1/accounts/acct-1/invoices/current", "")
	if malformed.StatusCode != http.StatusNotFound {
		t.Fatalf("malformed account path status = %d, want 404", malformed.StatusCode)
	}
}

type unavailableReadStore struct {
	store.Repository
}

func (s unavailableReadStore) LatestInvoice(context.Context, string) (model.InvoiceOverview, error) {
	return model.InvoiceOverview{}, fmt.Errorf("secret database details")
}

func (s unavailableReadStore) InvoiceSummary(context.Context, int) (model.InvoiceCounts, []model.InvoiceOverview, error) {
	return model.InvoiceCounts{}, nil, fmt.Errorf("secret database details")
}

func TestPrivateReads_MapStoreErrorsToSafeUnavailableResponse(t *testing.T) {
	repo := unavailableReadStore{Repository: store.NewMemory()}
	server := newReadServer(t, repo, httpapi.Config{})
	defer server.Close()
	for _, path := range []string{"/v1/accounts/acct-1/invoices/latest", "/v1/operator/summary"} {
		resp, body := getRead(t, server.URL+path, "")
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("GET %s status = %d body=%s, want 503", path, resp.StatusCode, body)
		}
		if strings.Contains(string(body), "secret database details") {
			t.Fatalf("GET %s leaked store error: %s", path, body)
		}
	}
}

func seedReadInvoice(t *testing.T, repo store.Repository, inv model.Invoice) {
	t.Helper()
	if err := repo.CreateInvoice(context.Background(), inv); err != nil {
		t.Fatalf("seed invoice %s: %v", inv.ID, err)
	}
}

func assertReadSecretsAbsent(t *testing.T, body []byte, values ...string) {
	t.Helper()
	text := string(body)
	for _, value := range values {
		if strings.Contains(text, value) {
			t.Fatalf("response leaked %q: %s", value, text)
		}
	}
}

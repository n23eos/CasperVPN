package btcpay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/model"
)

// sign computes the hex HMAC-SHA256 a real BTCPay server would send.
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestCreateAndLookupUseStableOrderID(t *testing.T) {
	const orderID = "billing-order-1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body createReq
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create: %v", err)
			}
			if body.Metadata["orderId"] != orderID {
				t.Errorf("orderId = %q, want %q", body.Metadata["orderId"], orderID)
			}
			_, _ = w.Write([]byte(`{"id":"provider-1","checkoutLink":"https://checkout/1"}`))
			return
		}
		if got := r.URL.Query().Get("orderId"); got != orderID {
			t.Errorf("lookup orderId = %q, want %q", got, orderID)
		}
		_, _ = w.Write([]byte(`[{"id":"provider-1","checkoutLink":"https://checkout/1","amount":"0.00010000","currency":"BTC","createdTime":1767225600,"expirationTime":1767227400,"metadata":{"orderId":"billing-order-1"}}]`))
	}))
	defer server.Close()
	g := New(Config{BaseURL: server.URL, APIKey: "key", StoreID: "store", WebhookSecret: "secret", Currencies: []string{"BTC"}})
	req := model.CreateInvoiceRequest{OrderID: orderID, AnonUserID: "acct", Plan: "basic", Currency: "BTC", Amount: "0.0001"}
	created, err := g.CreateInvoice(context.Background(), req)
	if err != nil || created.ID != orderID {
		t.Fatalf("create = %+v err=%v, want stable order id", created, err)
	}
	found, ok, err := g.LookupInvoice(context.Background(), req)
	if err != nil || !ok || found.ID != orderID || found.ProviderInvoiceID != "provider-1" {
		t.Fatalf("lookup = %+v ok=%t err=%v", found, ok, err)
	}
}

func TestLookupRejectsInvalidRemoteAmount(t *testing.T) {
	for _, amount := range []string{"not-a-number", "-0.0001"} {
		t.Run(amount, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`[{"id":"provider-1","amount":"` + amount + `","currency":"BTC","metadata":{"orderId":"billing-order-1"}}]`))
			}))
			defer server.Close()
			g := New(Config{BaseURL: server.URL, APIKey: "key", StoreID: "store", WebhookSecret: "secret"})
			_, _, err := g.LookupInvoice(context.Background(), model.CreateInvoiceRequest{
				OrderID: "billing-order-1", Currency: "BTC", Amount: "0.0001",
			})
			if err == nil {
				t.Fatalf("LookupInvoice accepted invalid remote amount %q", amount)
			}
		})
	}
}

func newGateway(secret string) *Gateway {
	return New(Config{
		BaseURL:       "https://btcpay.example.org",
		WebhookSecret: secret,
		Currencies:    []string{"BTC"},
	})
}

// A1: a configured gateway with an empty webhook secret must be rejected at
// startup, not run with a forgeable (empty-key) HMAC.
func TestConfigValidate_EmptySecretWithBaseURL(t *testing.T) {
	cfg := Config{BaseURL: "https://btcpay.example.org", APIKey: "key", StoreID: "store", WebhookSecret: ""}
	if err := cfg.Validate(); !errors.Is(err, ErrNoWebhookSecret) {
		t.Fatalf("Validate() = %v, want ErrNoWebhookSecret", err)
	}
	// A fully-unconfigured gateway (no base URL) is fine — it simply isn't wired.
	if err := (Config{}).Validate(); err != nil {
		t.Fatalf("unconfigured Validate() = %v, want nil", err)
	}
	if err := (Config{BaseURL: "https://x", APIKey: "key", StoreID: "store", WebhookSecret: "s"}).Validate(); err != nil {
		t.Fatalf("configured Validate() = %v, want nil", err)
	}
}

// A1: even with a valid-looking signature header, an empty secret must not verify
// a webhook (the empty HMAC key would otherwise accept forged events).
func TestParseWebhook_EmptySecretRejected(t *testing.T) {
	g := newGateway("")
	body := []byte(`{"deliveryId":"d1","type":"InvoiceSettled","metadata":{"orderId":"inv-1"}}`)
	// Sign with the (empty) secret the attacker would target.
	_, err := g.ParseWebhook(sign("", body), body)
	if !errors.Is(err, ErrNoWebhookSecret) {
		t.Fatalf("ParseWebhook with empty secret = %v, want ErrNoWebhookSecret", err)
	}
}

func TestParseWebhook_BadSignatureRejected(t *testing.T) {
	g := newGateway("topsecret")
	body := []byte(`{"deliveryId":"d1","type":"InvoiceSettled","metadata":{"orderId":"inv-1"}}`)
	if _, err := g.ParseWebhook("sha256=deadbeef", body); err == nil {
		t.Fatal("expected error for bad signature, got nil")
	}
}

// B4: InvoiceSettled activates (settled); InvoicePaymentSettled is a partial
// payment and must resolve to pending, never settled.
func TestParseWebhook_SettledVsPaymentSettled(t *testing.T) {
	g := newGateway("topsecret")

	settled := []byte(`{"deliveryId":"d1","type":"InvoiceSettled","metadata":{"orderId":"inv-1"}}`)
	ev, err := g.ParseWebhook(sign("topsecret", settled), settled)
	if err != nil {
		t.Fatalf("settled parse: %v", err)
	}
	if ev.Status != model.StatusSettled {
		t.Fatalf("InvoiceSettled status = %q, want settled", ev.Status)
	}

	partial := []byte(`{"deliveryId":"d2","type":"InvoicePaymentSettled","metadata":{"orderId":"inv-1"}}`)
	ev, err = g.ParseWebhook(sign("topsecret", partial), partial)
	if err != nil {
		t.Fatalf("partial parse: %v", err)
	}
	if ev.Status == model.StatusSettled {
		t.Fatalf("InvoicePaymentSettled must NOT be settled; got %q", ev.Status)
	}
	if ev.Status != model.StatusPending {
		t.Fatalf("InvoicePaymentSettled status = %q, want pending", ev.Status)
	}
}

// C9: the event timestamp comes from the BTCPay payload so the replay-window
// guard measures real event age, not the moment billing happened to parse it.
func TestParseWebhook_TimestampFromPayload(t *testing.T) {
	g := newGateway("topsecret")
	g.now = func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) }

	ts := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	body := []byte(`{"deliveryId":"d1","type":"InvoiceSettled","timestamp":` +
		itoa(ts.Unix()) + `,"metadata":{"orderId":"inv-1"}}`)

	ev, err := g.ParseWebhook(sign("topsecret", body), body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ev.Timestamp.Equal(ts) {
		t.Fatalf("event timestamp = %v, want %v (from payload)", ev.Timestamp, ts)
	}
}

// itoa avoids importing strconv just for the fixture body above.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

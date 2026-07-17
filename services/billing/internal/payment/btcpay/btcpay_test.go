package btcpay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	cfg := Config{BaseURL: "https://btcpay.example.org", WebhookSecret: ""}
	if err := cfg.Validate(); !errors.Is(err, ErrNoWebhookSecret) {
		t.Fatalf("Validate() = %v, want ErrNoWebhookSecret", err)
	}
	// A fully-unconfigured gateway (no base URL) is fine — it simply isn't wired.
	if err := (Config{}).Validate(); err != nil {
		t.Fatalf("unconfigured Validate() = %v, want nil", err)
	}
	if err := (Config{BaseURL: "https://x", WebhookSecret: "s"}).Validate(); err != nil {
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

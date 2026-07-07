package mock

import (
	"testing"

	"github.com/caspervpn/billing/internal/model"
)

func TestParseWebhook_ValidSignature(t *testing.T) {
	g := New("s3cr3t", []string{"BTC"})
	body := []byte(`{"external_id":"d1","invoice_id":"inv-1","status":"settled","amount":"0.0001","currency":"BTC","confirmations":3}`)
	sig := g.Sign(body)

	ev, err := g.ParseWebhook(sig, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.InvoiceID != "inv-1" || ev.Status != model.StatusSettled {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestParseWebhook_BadSignatureRejected(t *testing.T) {
	g := New("s3cr3t", []string{"BTC"})
	body := []byte(`{"external_id":"d1","invoice_id":"inv-1","status":"settled"}`)

	if _, err := g.ParseWebhook("deadbeef", body); err == nil {
		t.Fatal("expected error for bad signature")
	}
	// Tampered body must also fail against a signature computed for the original.
	sig := g.Sign(body)
	if _, err := g.ParseWebhook(sig, append(body, ' ')); err == nil {
		t.Fatal("expected error for tampered body")
	}
}

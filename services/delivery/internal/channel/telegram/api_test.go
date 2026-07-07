package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type recordDoer struct {
	status  int
	lastURL string
	lastCT  string
	body    string
	err     error
}

func (d *recordDoer) Do(req *http.Request) (*http.Response, error) {
	d.lastURL = req.URL.String()
	d.lastCT = req.Header.Get("Content-Type")
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		d.body = string(b)
	}
	if d.err != nil {
		return nil, d.err
	}
	return &http.Response{
		StatusCode: d.status,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}, nil
}

func TestTelegramAPISend(t *testing.T) {
	d := &recordDoer{status: http.StatusOK}
	api := TelegramAPI{Base: "https://api.telegram.org", Token: "TOK", HTTP: d}
	if api.Network() != "telegram" {
		t.Fatalf("network = %q", api.Network())
	}
	if err := api.Send(context.Background(), 42, "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(d.lastURL, "/botTOK/sendMessage") {
		t.Fatalf("url = %q", d.lastURL)
	}
	if !strings.Contains(d.body, "chat_id=42") || !strings.Contains(d.body, "text=hi") {
		t.Fatalf("form body = %q", d.body)
	}
}

func TestTelegramAPIRequiresConfig(t *testing.T) {
	if err := (TelegramAPI{}).Send(context.Background(), 1, "x"); err == nil {
		t.Fatalf("expected error without base/token")
	}
}

func TestTelegramAPINon200IsError(t *testing.T) {
	api := TelegramAPI{Base: "https://api.telegram.org", Token: "TOK", HTTP: &recordDoer{status: http.StatusForbidden}}
	if err := api.Send(context.Background(), 1, "x"); err == nil {
		t.Fatalf("expected error on non-200")
	}
}

func TestMaxAPISend(t *testing.T) {
	d := &recordDoer{status: http.StatusOK}
	api := MaxAPI{Base: "https://botapi.max.ru", Token: "MTOK", HTTP: d}
	if api.Network() != "max" {
		t.Fatalf("network = %q", api.Network())
	}
	if err := api.Send(context.Background(), 7, "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(d.lastURL, "access_token=MTOK") || !strings.Contains(d.lastURL, "chat_id=7") {
		t.Fatalf("url = %q", d.lastURL)
	}
	if d.lastCT != "application/json" || !strings.Contains(d.body, `"text":"hello"`) {
		t.Fatalf("ct=%q body=%q", d.lastCT, d.body)
	}
}

func TestMaxAPIRequiresConfig(t *testing.T) {
	if err := (MaxAPI{}).Send(context.Background(), 1, "x"); err == nil {
		t.Fatalf("expected error without base/token")
	}
}

func TestSendTransportErrorPropagates(t *testing.T) {
	api := TelegramAPI{Base: "b", Token: "t", HTTP: &recordDoer{err: errors.New("net")}}
	if err := api.Send(context.Background(), 1, "x"); err == nil {
		t.Fatalf("expected transport error")
	}
}

func TestNewBotValidation(t *testing.T) {
	if _, err := NewBot(nil, fakeProvider{}, BotConfig{}); err == nil {
		t.Fatalf("expected error for nil api")
	}
	if _, err := NewBot(&fakeAPI{}, nil, BotConfig{}); err == nil {
		t.Fatalf("expected error for nil provider")
	}
	// Zero config applies defaults.
	if _, err := NewBot(&fakeAPI{}, fakeProvider{}, BotConfig{}); err != nil {
		t.Fatalf("defaults should apply: %v", err)
	}
}

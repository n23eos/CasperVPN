package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recordDoer struct {
	status   int
	lastURL  string
	lastCT   string
	body     string
	response string
	err      error
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
	response := d.response
	if response == "" {
		response = `{"ok":true}`
	}
	return &http.Response{
		StatusCode: d.status,
		Body:       io.NopCloser(strings.NewReader(response)),
		Header:     make(http.Header),
	}, nil
}

func TestAPISend(t *testing.T) {
	d := &recordDoer{status: http.StatusOK}
	api := API{Base: "https://api.telegram.org", Token: "TOK", HTTP: d}
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

func TestAPIRequiresConfig(t *testing.T) {
	if err := (API{}).Send(context.Background(), 1, "x"); err == nil {
		t.Fatalf("expected error without base/token")
	}
}

func TestAPINon200IsError(t *testing.T) {
	api := API{Base: "https://api.telegram.org", Token: "TOK", HTTP: &recordDoer{status: http.StatusForbidden}}
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
	api := API{Base: "b", Token: "t", HTTP: &recordDoer{err: errors.New("net")}}
	if err := api.Send(context.Background(), 1, "x"); err == nil {
		t.Fatalf("expected transport error")
	}
}

func TestNewBotValidation(t *testing.T) {
	onboarding := &fakeOnboarding{}
	if _, err := NewBot(nil, onboarding, BotConfig{DefaultCurrency: "XMR"}); err == nil {
		t.Fatalf("expected error for nil api")
	}
	if _, err := NewBot(&fakeAPI{}, nil, BotConfig{DefaultCurrency: "XMR"}); err == nil {
		t.Fatalf("expected error for nil onboarding")
	}
	if _, err := NewBot(&fakeAPI{}, onboarding, BotConfig{DefaultCurrency: "XMR"}); err != nil {
		t.Fatalf("defaults should apply: %v", err)
	}
}

func TestAPIGetUpdatesUsesSenderAndPrivateChatMetadata(t *testing.T) {
	doer := &recordDoer{status: http.StatusOK, response: `{"ok":true,"result":[{"update_id":9,"message":{"from":{"id":42},"chat":{"id":777,"type":"private"},"text":"/get 999"}}]}`}
	api := API{Base: "https://telegram.invalid", Token: "TOKEN", HTTP: doer}
	updates, err := api.GetUpdates(context.Background(), 9, 25*time.Second)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 1 || updates[0].UpdateID != 9 || updates[0].UserID != 42 || updates[0].ChatID != 777 || updates[0].ChatType != "private" {
		t.Fatalf("updates = %+v", updates)
	}
	if !strings.Contains(doer.body, "offset=9") || !strings.Contains(doer.body, "timeout=25") {
		t.Fatalf("poll form = %q", doer.body)
	}
}

func TestAPIBoundsResponseBody(t *testing.T) {
	doer := &recordDoer{status: http.StatusOK, response: strings.Repeat("x", maxAPIResponseBytes+1)}
	api := API{Base: "https://telegram.invalid", Token: "TOKEN", HTTP: doer}
	if _, err := api.GetUpdates(context.Background(), 1, time.Second); err == nil {
		t.Fatal("oversized response must fail")
	}
}

func TestFakeTelegramNetworkPrivateSenderFlow(t *testing.T) {
	var sentChatID string
	var sentText string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/botTOKEN/getUpdates":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"result":[{"update_id":15,"message":{"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/get 777"}}]}`)
		case "/botTOKEN/sendMessage":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse send form: %v", err)
			}
			sentChatID = r.Form.Get("chat_id")
			sentText = r.Form.Get("text")
			_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api := API{Base: server.URL, Token: "TOKEN", HTTP: server.Client()}
	updates, err := api.GetUpdates(context.Background(), 15, time.Second)
	if err != nil || len(updates) != 1 {
		t.Fatalf("GetUpdates = %+v, %v", updates, err)
	}
	onboarding := &fakeOnboarding{link: Link{SubscriptionURL: "https://subscriptions.example/sub/stable"}}
	bot := newTestBot(t, api, onboarding, time.Now)
	if err := bot.HandleUpdate(context.Background(), updates[0]); err != nil {
		t.Fatalf("HandleUpdate: %v", err)
	}
	if len(onboarding.linkUser) != 1 || onboarding.linkUser[0] != 42 {
		t.Fatalf("sender identity = %v", onboarding.linkUser)
	}
	if sentChatID != "42" || !strings.Contains(sentText, "/sub/stable") {
		t.Fatalf("sendMessage chat=%q text=%q", sentChatID, sentText)
	}
}

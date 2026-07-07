// Package telegram implements the messenger delivery channel: a bot that hands a
// user their current subscription URL / Happ deep-link on command, plus the same
// bot acting as a generic artifact channel (it can serve the signed directory
// blob). A Max-compatible adapter rides the SAME bot logic — only the send API
// differs — so the messenger channel is itself redundant across two networks.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// defaultHTTPClient is the bounded-timeout fallback when no client is injected, so
// a stalled Bot API endpoint can't hang a send forever (http.DefaultClient has no
// timeout).
var defaultHTTPClient = &http.Client{Timeout: 10 * time.Second}

// Update is the minimal inbound event the bot reacts to: who sent what.
type Update struct {
	ChatID int64  // where to reply
	UserID int64  // the telegram/max user id (maps to contracts.User.TelegramID)
	Text   string // message text / command
}

// BotAPI is the outbound send surface. Two implementations (Telegram, Max) let
// the same handler serve two messenger networks; tests inject a fake. No bot
// token or API base is hardcoded — both come from config.
type BotAPI interface {
	// Send delivers text to a chat. Network/name is the adapter's concern.
	Send(ctx context.Context, chatID int64, text string) error
	// Network names the messenger ("telegram"/"max") for logs and telemetry.
	Network() string
}

// httpDoer is the http.Client slice this package needs (mockable in tests).
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// TelegramAPI is the real Bot API adapter. Base + Token come from config.
type TelegramAPI struct {
	Base  string   // e.g. https://api.telegram.org — config-supplied, never hardcoded
	Token string   // bot token — from env/secret manager (security.md)
	HTTP  httpDoer // nil => http.DefaultClient
}

// Network identifies the messenger.
func (t TelegramAPI) Network() string { return "telegram" }

// Send posts a sendMessage call to the Telegram Bot API.
func (t TelegramAPI) Send(ctx context.Context, chatID int64, text string) error {
	if t.Base == "" || t.Token == "" {
		return fmt.Errorf("telegram: base and token required")
	}
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", t.Base, t.Token)
	form := url.Values{}
	form.Set("chat_id", fmt.Sprintf("%d", chatID))
	form.Set("text", text)
	return postForm(ctx, t.HTTP, endpoint, form)
}

// MaxAPI is the Max messenger adapter. Same shape, different endpoint scheme, so
// the identical bot logic serves Max users too.
type MaxAPI struct {
	Base  string   // Max bot API base — config-supplied
	Token string   // bot token — from env/secret manager
	HTTP  httpDoer // nil => http.DefaultClient
}

// Network identifies the messenger.
func (m MaxAPI) Network() string { return "max" }

// Send posts a message via the Max bot API (JSON body, token as query param).
func (m MaxAPI) Send(ctx context.Context, chatID int64, text string) error {
	if m.Base == "" || m.Token == "" {
		return fmt.Errorf("max: base and token required")
	}
	endpoint := fmt.Sprintf("%s/messages?access_token=%s&chat_id=%d", m.Base, url.QueryEscape(m.Token), chatID)
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doExpectOK(ctx, m.HTTP, req)
}

func postForm(ctx context.Context, doer httpDoer, endpoint string, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doExpectOK(ctx, doer, req)
}

func doExpectOK(_ context.Context, doer httpDoer, req *http.Request) error {
	if doer == nil {
		doer = defaultHTTPClient
	}
	resp, err := doer.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: send status %d", req.URL.Host, resp.StatusCode)
	}
	return nil
}

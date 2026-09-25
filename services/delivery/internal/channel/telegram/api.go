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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// defaultHTTPClient is the bounded-timeout fallback when no client is injected, so
// a stalled Bot API endpoint can't hang a send forever (http.DefaultClient has no
// timeout).
var defaultHTTPClient = &http.Client{Timeout: 40 * time.Second}

const maxAPIResponseBytes = 64 << 10

// Update is the minimal inbound event the bot reacts to: who sent what.
type Update struct {
	UpdateID int64
	ChatID   int64
	ChatType string
	UserID   int64
	Text     string
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

// API is the real Bot API adapter. Base + Token come from config.
type API struct {
	Base  string   // e.g. https://api.telegram.org — config-supplied, never hardcoded
	Token string   // bot token — from env/secret manager (security.md)
	HTTP  httpDoer // nil => http.DefaultClient
}

// Network identifies the messenger.
func (t API) Network() string { return "telegram" }

// Send posts a sendMessage call to the Telegram Bot API.
func (t API) Send(ctx context.Context, chatID int64, text string) error {
	if t.Base == "" || t.Token == "" {
		return fmt.Errorf("telegram: base and token required")
	}
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", t.Base, t.Token)
	form := url.Values{}
	form.Set("chat_id", fmt.Sprintf("%d", chatID))
	form.Set("text", text)
	return postTelegramForm(ctx, t.HTTP, endpoint, form, nil)
}

// GetUpdates performs one bounded Telegram long-poll request. Sender identity
// comes exclusively from message.from.id; command text is never trusted for it.
func (t API) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	if t.Base == "" || t.Token == "" {
		return nil, errors.New("telegram: base and token required")
	}
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates", t.Base, t.Token)
	form := url.Values{}
	form.Set("offset", strconv.FormatInt(offset, 10))
	seconds := int64(timeout / time.Second)
	if timeout%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	form.Set("timeout", strconv.FormatInt(seconds, 10))

	var response struct {
		Result []struct {
			UpdateID int64 `json:"update_id"`
			Message *struct {
				From *struct {
					ID int64 `json:"id"`
				} `json:"from"`
				Chat struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
				Text string `json:"text"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := postTelegramForm(ctx, t.HTTP, endpoint, form, &response); err != nil {
		return nil, err
	}
	updates := make([]Update, 0, len(response.Result))
	for _, raw := range response.Result {
		u := Update{UpdateID: raw.UpdateID}
		if raw.Message != nil {
			u.ChatID = raw.Message.Chat.ID
			u.ChatType = raw.Message.Chat.Type
			u.Text = raw.Message.Text
			if raw.Message.From != nil {
				u.UserID = raw.Message.From.ID
			}
		}
		updates = append(updates, u)
	}
	return updates, nil
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
	return doExpectStatus(m.HTTP, req)
}

func postTelegramForm(ctx context.Context, doer httpDoer, endpoint string, form url.Values, dst interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return errors.New("telegram: create request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if doer == nil {
		doer = defaultHTTPClient
	}
	resp, err := doer.Do(req)
	if err != nil {
		return errors.New("telegram: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: API status %d", resp.StatusCode)
	}
	body, err := readBounded(resp.Body)
	if err != nil {
		return err
	}
	var envelope struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || !envelope.OK {
		return errors.New("telegram: invalid API response")
	}
	if dst != nil {
		if err := json.Unmarshal(body, dst); err != nil {
			return errors.New("telegram: invalid result")
		}
	}
	return nil
}

func doExpectStatus(doer httpDoer, req *http.Request) error {
	if doer == nil {
		doer = defaultHTTPClient
	}
	resp, err := doer.Do(req)
	if err != nil {
		return errors.New("messenger: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("messenger: API status %d", resp.StatusCode)
	}
	return nil
}

func readBounded(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxAPIResponseBytes+1))
	if err != nil {
		return nil, errors.New("telegram: read API response")
	}
	if len(body) > maxAPIResponseBytes {
		return nil, errors.New("telegram: API response too large")
	}
	return body, nil
}

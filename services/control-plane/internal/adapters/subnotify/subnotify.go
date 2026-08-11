// Package subnotify is the HTTP adapter behind domain.SubscriptionRevoker: it
// pushes token registration/revocation into the subscription service's
// /internal/* endpoints so a ban/cancel/rotate takes effect immediately (index
// entry removed + render cache flushed) instead of waiting out the cache TTL.
//
// Configuration comes from env (SUBSCRIPTION_INTERNAL_URL + token). When the
// URL is unset the Noop implementation is wired instead, so dev/local runs
// don't break. Every call is bounded by a timeout; retries/circuit-breaking is
// deliberately left to TZ-hardening.
package subnotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/caspervpn/platform/httpx"
)

// Notifier calls the subscription service's internal API.
type Notifier struct {
	baseURL string
	token   string
	client  *http.Client
}

// New builds a Notifier. baseURL is e.g. http://subscription:8082; token guards
// the /internal/* endpoints (bearer). timeout bounds each outgoing call.
func New(baseURL, token string, timeout time.Duration) *Notifier {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Notifier{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		client:  &http.Client{Timeout: timeout},
	}
}

// revokePayload matches the subscription service's /internal/revoke body.
// Exactly one selector is set per call.
type revokePayload struct {
	Token          string `json:"token,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	SubscriptionID string `json:"subscription_id,omitempty"`
}

// registerPayload matches the subscription service's /internal/tokens body.
type registerPayload struct {
	Token          string `json:"token"`
	UserID         string `json:"user_id"`
	SubscriptionID string `json:"subscription_id"`
}

// RevokeUser implements domain.SubscriptionRevoker.
func (n *Notifier) RevokeUser(ctx context.Context, userID string) error {
	return n.post(ctx, "/internal/revoke", revokePayload{UserID: userID})
}

// RevokeSubscription implements domain.SubscriptionRevoker.
func (n *Notifier) RevokeSubscription(ctx context.Context, subscriptionID string) error {
	return n.post(ctx, "/internal/revoke", revokePayload{SubscriptionID: subscriptionID})
}

// RegisterToken implements domain.SubscriptionRevoker.
func (n *Notifier) RegisterToken(ctx context.Context, token, userID, subscriptionID string) error {
	return n.post(ctx, "/internal/tokens", registerPayload{
		Token: token, UserID: userID, SubscriptionID: subscriptionID,
	})
}

func (n *Notifier) post(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("subnotify: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("subnotify: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+n.token)

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("subnotify: %s: %w", path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, httpx.MaxBody))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("subnotify: %s: unexpected status %d", path, resp.StatusCode)
	}
	return nil
}

// Noop satisfies domain.SubscriptionRevoker when no subscription service is
// configured (dev/local). It does nothing and never fails.
type Noop struct{}

// RevokeUser implements domain.SubscriptionRevoker.
func (Noop) RevokeUser(context.Context, string) error { return nil }

// RevokeSubscription implements domain.SubscriptionRevoker.
func (Noop) RevokeSubscription(context.Context, string) error { return nil }

// RegisterToken implements domain.SubscriptionRevoker.
func (Noop) RegisterToken(context.Context, string, string, string) error { return nil }

package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/platform/httpx"
)

// HTTPClient is the real control-plane adapter. The bearer token authenticates
// internal service-to-service calls; it comes from env/secret manager. Every call
// carries a bounded timeout so a slow control-plane cannot wedge billing.
type HTTPClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewHTTPClient builds an adapter against baseURL (e.g. http://control-plane:8081).
func NewHTTPClient(baseURL, token string) *HTTPClient {
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *HTTPClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("controlplane: marshal: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("controlplane: new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("controlplane: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return &statusError{method: method, path: path, code: resp.StatusCode, body: string(b)}
	}
	if out != nil {
		// Bounded read: never trust a peer (even the control-plane) for body size.
		if err := httpx.DecodeJSON(resp.Body, out); err != nil {
			return fmt.Errorf("controlplane: decode: %w", err)
		}
	}
	return nil
}

// GetUser implements Client.
func (c *HTTPClient) GetUser(ctx context.Context, id string) (contracts.User, error) {
	var u contracts.User
	err := c.do(ctx, http.MethodGet, "/v1/users/"+id, nil, &u)
	return u, err
}

// CreateSubscription implements Client (POST /v1/subscriptions).
func (c *HTTPClient) CreateSubscription(ctx context.Context, userID string, plan contracts.SubscriptionPlan) (contracts.Subscription, error) {
	body := map[string]any{"user_id": userID, "plan": plan}
	var s contracts.Subscription
	if err := c.do(ctx, http.MethodPost, "/v1/subscriptions", body, &s); err != nil {
		if statusCode(err) == http.StatusConflict {
			return contracts.Subscription{}, ErrSubscriptionExists
		}
		return contracts.Subscription{}, err
	}
	return s, nil
}

// SetSubscriptionPeriod implements Client (additive PATCH /v1/subscriptions/{id}).
func (c *HTTPClient) SetSubscriptionPeriod(ctx context.Context, subID string, status contracts.SubscriptionStatus, expiresAt time.Time) (contracts.Subscription, error) {
	body := map[string]any{"status": status, "expires_at": expiresAt}
	var s contracts.Subscription
	err := c.do(ctx, http.MethodPatch, "/v1/subscriptions/"+subID, body, &s)
	return s, err
}

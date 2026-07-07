package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/caspervpn/contracts"
)

// HTTPClient is a Provider backed by the control-plane REST API (frozen
// OpenAPI). The base URL and bearer token are injected from config — no host is
// hardcoded.
type HTTPClient struct {
	base   string
	token  string
	client *http.Client
}

// NewHTTPClient builds a control-plane HTTP Provider. base is the API root
// (e.g. http://control-plane:8081); bearer is the internal service token.
func NewHTTPClient(base, bearer string, timeout time.Duration) *HTTPClient {
	return &HTTPClient{
		base:   base,
		token:  bearer,
		client: &http.Client{Timeout: timeout},
	}
}

// GetUser implements Provider via GET /v1/users/{id}.
func (c *HTTPClient) GetUser(ctx context.Context, id string) (contracts.User, error) {
	var u contracts.User
	err := c.getJSON(ctx, "/v1/users/"+url.PathEscape(id), &u)
	return u, err
}

// GetSubscription implements Provider via GET /v1/subscriptions/{id}.
func (c *HTTPClient) GetSubscription(ctx context.Context, id string) (contracts.Subscription, error) {
	var s contracts.Subscription
	err := c.getJSON(ctx, "/v1/subscriptions/"+url.PathEscape(id), &s)
	return s, err
}

// nodePage mirrors the control-plane list envelope.
type nodePage struct {
	Items      []contracts.Node `json:"items"`
	NextCursor string           `json:"next_cursor"`
}

// ListNodes implements Provider via GET /v1/nodes, following cursor pagination.
func (c *HTTPClient) ListNodes(ctx context.Context, f NodeFilter) ([]contracts.Node, error) {
	var all []contracts.Node
	cursor := ""
	for {
		q := url.Values{}
		if f.Status != "" {
			q.Set("status", string(f.Status))
		}
		if f.Role != "" {
			q.Set("role", string(f.Role))
		}
		if f.Region != "" {
			q.Set("region", f.Region)
		}
		if f.Limit > 0 {
			q.Set("limit", strconv.Itoa(f.Limit))
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page nodePage
		if err := c.getJSON(ctx, "/v1/nodes?"+q.Encode(), &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.NextCursor == "" || (f.Limit > 0 && len(all) >= f.Limit) {
			break
		}
		cursor = page.NextCursor
	}
	return all, nil
}

func (c *HTTPClient) getJSON(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("controlplane %s: %w", path, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode >= 400:
		return fmt.Errorf("controlplane %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("controlplane %s: decode: %w", path, err)
	}
	return nil
}

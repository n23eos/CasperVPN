package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/platform/httpx"
)

// AuthoritativeIndex keeps legacy callbacks durable but never trusts their
// possibly stale local view when authorizing a public subscription request.
type AuthoritativeIndex struct {
	TokenIndex
	CP *HTTPClient
}

func (a *AuthoritativeIndex) Lookup(ctx context.Context, token string) (string, string, error) {
	if token == "" || len(token) > 512 {
		return "", "", ErrNotFound
	}
	body, err := json.Marshal(contracts.ResolveSubscriptionToken{TokenHash: HashToken(token)})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.CP.base+"/v1/subscription-tokens/resolve", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.CP.token)
	resp, err := a.CP.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("token authority unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return "", "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("token authority returned status %d", resp.StatusCode)
	}
	var binding contracts.SubscriptionTokenBinding
	if err := httpx.DecodeJSON(resp.Body, &binding); err != nil {
		return "", "", fmt.Errorf("invalid token authority response")
	}
	if binding.UserID == "" || binding.SubscriptionID == "" {
		return "", "", ErrNotFound
	}
	return binding.UserID, binding.SubscriptionID, nil
}

// Ready checks the upstream's actual readiness, without requiring a user token.
func (c *HTTPClient) Ready(ctx context.Context) error {
	var response map[string]interface{}
	return c.getJSON(ctx, "/readyz", &response)
}

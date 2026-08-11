// Package telemetryclient is the HTTP adapter behind ports.TelemetryClient:
// GET /v1/recommendations (feedback in) and POST /v1/health (authoritative
// probe verdicts out). Both endpoints are bearer-protected.
package telemetryclient

import (
	"context"
	"fmt"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/platform/httpx"
)

// Client talks to the telemetry service.
type Client struct {
	hc *httpx.Client
}

// New builds a Client for the telemetry base URL.
func New(baseURL, token string, timeout time.Duration) *Client {
	return &Client{hc: httpx.New(baseURL, token, timeout)}
}

// Recommendations fetches the latest evaluation window payload.
func (c *Client) Recommendations(ctx context.Context) (contracts.Recommendations, error) {
	var out contracts.Recommendations
	if err := c.hc.GetJSON(ctx, "/v1/recommendations", &out); err != nil {
		return contracts.Recommendations{}, fmt.Errorf("telemetry: recommendations: %w", err)
	}
	return out, nil
}

// ReportHealth submits one authoritative HealthEvent from an own probe.
func (c *Client) ReportHealth(ctx context.Context, ev contracts.HealthEvent) error {
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("telemetry: refusing to send invalid health event: %w", err)
	}
	if err := c.hc.PostJSON(ctx, "/v1/health", ev, nil); err != nil {
		return fmt.Errorf("telemetry: report health: %w", err)
	}
	return nil
}

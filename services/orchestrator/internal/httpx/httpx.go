// Package httpx is the shared HTTP plumbing for the orchestrator's outbound
// clients: bearer auth, bounded timeouts, JSON decoding with size limits, and
// a small bounded retry for idempotent requests. No infinite loops: every
// retry budget is finite and every attempt is context-bounded.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxBody caps response bodies (defensive against a compromised peer).
const maxBody = 8 << 20 // 8 MiB

// Client wraps http.Client with base URL + bearer token.
type Client struct {
	base    string
	token   string
	http    *http.Client
	retries int           // extra attempts for idempotent requests
	backoff time.Duration // pause between attempts
}

// New builds a Client. timeout bounds every single attempt.
func New(baseURL, token string, timeout time.Duration) *Client {
	return &Client{
		base:    baseURL,
		token:   token,
		http:    &http.Client{Timeout: timeout},
		retries: 2,
		backoff: 500 * time.Millisecond,
	}
}

// StatusError is a non-2xx response.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("httpx: unexpected status %d: %.200s", e.Code, e.Body)
}

// retryable: transport errors and 5xx are worth one more try for idempotent
// verbs; 4xx never is (auth/contract problem — retrying can't fix it).
func retryable(err error, code int) bool {
	if err != nil {
		return true
	}
	return code >= 500
}

// GetJSON GETs path and decodes the JSON response into out. Idempotent —
// retried within the finite budget.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, out, true)
}

// PostJSON POSTs body as JSON. NOT retried (POST is not idempotent here).
func (c *Client) PostJSON(ctx context.Context, path string, body, out any) error {
	return c.doJSON(ctx, http.MethodPost, path, body, out, false)
}

// PatchJSON PATCHes body as JSON. Our PATCH carries the full object (the
// control-plane replaces it), so it is idempotent and safe to retry.
func (c *Client) PatchJSON(ctx context.Context, path string, body, out any) error {
	return c.doJSON(ctx, http.MethodPatch, path, body, out, true)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any, idempotent bool) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("httpx: encode %s %s: %w", method, path, err)
		}
	}
	attempts := 1
	if idempotent {
		attempts += c.retries
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.backoff):
			}
		}
		code, respBody, err := c.attempt(ctx, method, path, payload)
		if err == nil && code >= 200 && code < 300 {
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("httpx: %s %s: malformed JSON response: %w", method, path, err)
			}
			return nil
		}
		if err != nil {
			lastErr = fmt.Errorf("httpx: %s %s: %w", method, path, err)
		} else {
			lastErr = &StatusError{Code: code, Body: string(respBody)}
		}
		if !retryable(err, code) {
			return lastErr
		}
	}
	return lastErr
}

func (c *Client) attempt(ctx context.Context, method, path string, payload []byte) (int, []byte, error) {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
}

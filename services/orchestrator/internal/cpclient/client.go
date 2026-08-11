// Package cpclient is the HTTP adapter behind ports.ControlPlaneClient: the
// orchestrator's read/write view of the control-plane node registry
// (GET/PATCH /v1/nodes...). Requires an orchestrator-role service token.
package cpclient

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/orchestrator/internal/ports"
	"github.com/caspervpn/platform/httpx"
)

// Client talks to the control-plane.
type Client struct {
	hc *httpx.Client
}

// New builds a Client for the control-plane base URL.
func New(baseURL, token string, timeout time.Duration) *Client {
	return &Client{hc: httpx.New(baseURL, token, timeout)}
}

// nodesPage mirrors the control-plane GET /v1/nodes response.
type nodesPage struct {
	Items      []contracts.Node `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// ListNodes fetches all nodes matching the filter, walking pagination with a
// finite page budget (no infinite cursor loops on a misbehaving peer).
func (c *Client) ListNodes(ctx context.Context, f ports.NodeFilter) ([]contracts.Node, error) {
	const maxPages = 100
	var (
		nodes  []contracts.Node
		cursor string
	)
	for page := 0; page < maxPages; page++ {
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
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		path := "/v1/nodes"
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}
		var pg nodesPage
		if err := c.hc.GetJSON(ctx, path, &pg); err != nil {
			return nil, fmt.Errorf("control-plane: list nodes: %w", err)
		}
		nodes = append(nodes, pg.Items...)
		if pg.NextCursor == "" {
			return nodes, nil
		}
		cursor = pg.NextCursor
	}
	return nil, fmt.Errorf("control-plane: list nodes: pagination exceeded %d pages", maxPages)
}

// GetNode fetches one node by ID.
func (c *Client) GetNode(ctx context.Context, id string) (contracts.Node, error) {
	var n contracts.Node
	if err := c.hc.GetJSON(ctx, "/v1/nodes/"+url.PathEscape(id), &n); err != nil {
		return contracts.Node{}, fmt.Errorf("control-plane: get node %s: %w", id, err)
	}
	return n, nil
}

// UpdateNode PATCHes the full Node object (idempotent full replacement).
func (c *Client) UpdateNode(ctx context.Context, n contracts.Node) (contracts.Node, error) {
	if err := n.Validate(); err != nil {
		return contracts.Node{}, fmt.Errorf("control-plane: refusing to send invalid node: %w", err)
	}
	var out contracts.Node
	if err := c.hc.PatchJSON(ctx, "/v1/nodes/"+url.PathEscape(n.ID), n, &out); err != nil {
		return contracts.Node{}, fmt.Errorf("control-plane: update node %s: %w", n.ID, err)
	}
	return out, nil
}

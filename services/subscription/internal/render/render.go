package render

import (
	"errors"
	"fmt"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/subscription/internal/config"
)

// ErrUnavailable prevents an outage from becoming a direct-only VPN profile.
var ErrUnavailable = errors.New("render: no usable personal VPN transports")

// Content types per representation.
const (
	CTBase64  = "text/plain; charset=utf-8"
	CTSingBox = "application/json; charset=utf-8"
	CTClash   = "application/yaml; charset=utf-8"
)

// Renderer produces the wire payload for a bundle in a chosen format. It holds
// the split-tunnel routing policy injected into the sing-box and Clash outputs.
type Renderer struct {
	policy config.RoutingPolicy
}

// New builds a Renderer bound to a routing policy.
func New(policy config.RoutingPolicy) *Renderer {
	return &Renderer{policy: policy}
}

// Render returns the payload bytes and Content-Type for the format.
func (r *Renderer) Render(f Format, b contracts.SubscriptionBundle) ([]byte, string, error) {
	available := false
	for _, n := range b.Nodes {
		for _, transport := range n.Transports {
			if transport.Enabled && (f != FormatBase64 || transport.Type != contracts.TransportAmneziaWG) {
				available = true
			}
		}
	}
	if !available {
		return nil, "", ErrUnavailable
	}
	switch f {
	case FormatBase64:
		return []byte(b.ToBase64List()), CTBase64, nil
	case FormatSingBox:
		body, err := singBoxDocument(b, r.policy)
		if err != nil {
			return nil, "", fmt.Errorf("render sing-box: %w", err)
		}
		return body, CTSingBox, nil
	case FormatClash:
		body, err := clashDocument(b, r.policy)
		if err != nil {
			return nil, "", fmt.Errorf("render clash: %w", err)
		}
		return body, CTClash, nil
	}
	return nil, "", fmt.Errorf("render: unknown format %q", f)
}

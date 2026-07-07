package config

import (
	"encoding/json"
	"fmt"
)

// Reserved outbound / proxy-group tags the renderer injects. A routing policy
// references these by name (e.g. send RU traffic to "direct", everything else to
// the "proxy" selector). Kept here so config authors and the renderer agree.
const (
	// TagDirect is the split-tunnel bypass outbound (traffic leaves untunneled).
	TagDirect = "direct"
	// TagProxy is the selector grouping every node transport (the tunnel).
	TagProxy = "proxy"
	// TagAuto is the latency urltest group feeding the fallback cascade.
	TagAuto = "auto"
	// ClashProxyGroup is the Clash proxy-group name that mirrors TagProxy.
	ClashProxyGroup = "PROXY"
	// ClashAutoGroup is the Clash url-test group mirroring TagAuto.
	ClashAutoGroup = "AUTO"
)

// RoutingPolicy is the split-tunnel policy rendered into both sing-box and
// Clash. It carries engine-native fragments verbatim (pass-through) so the
// censorship-relevant details — RU domain/geo categories, rule-set URLs, DNS
// servers, the probe URL — live in DATA, never in Go code.
type RoutingPolicy struct {
	// ProbeURL is the connectivity-check URL the urltest group uses to rank
	// transports (config-supplied, e.g. a generate_204 endpoint).
	ProbeURL string `json:"probe_url"`
	// ProbeInterval is the urltest re-check interval (Go/sing-box duration).
	ProbeInterval string `json:"probe_interval"`

	SingBox SingBoxRouting `json:"singbox"`
	Clash   ClashRouting   `json:"clash"`
}

// SingBoxRouting holds verbatim sing-box route/dns fragments.
type SingBoxRouting struct {
	// DNS is a verbatim sing-box "dns" object (optional).
	DNS json.RawMessage `json:"dns,omitempty"`
	// RuleSets are verbatim entries for route.rule_set (remote geosite/geoip srs).
	RuleSets []map[string]interface{} `json:"rule_sets,omitempty"`
	// Rules are verbatim route.rules entries evaluated before Final (e.g. send
	// RU geosite/geoip to "direct").
	Rules []map[string]interface{} `json:"rules,omitempty"`
	// Final is the fallthrough outbound tag (default TagProxy).
	Final string `json:"final,omitempty"`
}

// ClashRouting holds verbatim Clash.Meta routing fragments.
type ClashRouting struct {
	// DNS is a verbatim Clash "dns" mapping (optional).
	DNS map[string]interface{} `json:"dns,omitempty"`
	// RuleProviders is a verbatim Clash "rule-providers" mapping.
	RuleProviders map[string]interface{} `json:"rule_providers,omitempty"`
	// Rules are verbatim Clash rule lines evaluated before the final MATCH (which
	// the renderer appends, pointing at ClashProxyGroup).
	Rules []string `json:"rules,omitempty"`
}

// Validate applies defaults and checks the policy is renderable.
func (r *RoutingPolicy) Validate() error {
	if r.ProbeURL == "" {
		return fmt.Errorf("config: routing policy requires probe_url (config-supplied, never hardcoded)")
	}
	if r.ProbeInterval == "" {
		r.ProbeInterval = "3m"
	}
	if r.SingBox.Final == "" {
		r.SingBox.Final = TagProxy
	}
	return nil
}

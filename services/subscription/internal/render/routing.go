package render

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/subscription/internal/config"
)

// singBoxDocument wraps the frozen contract outbounds into a full sing-box
// config: it adds an urltest ("auto") + selector ("proxy") for the fallback
// cascade, a "direct" outbound, and the split-tunnel route/dns from policy.
func singBoxDocument(b contracts.SubscriptionBundle, p config.RoutingPolicy) ([]byte, error) {
	outbounds := b.ToSingBox().Outbounds
	tags := singBoxProxyTags(outbounds)

	route := map[string]interface{}{"auto_detect_interface": true}

	if len(tags) == 0 {
		// No servable node: emit a valid direct-only document.
		outbounds = append(outbounds, map[string]interface{}{"type": "direct", "tag": config.TagDirect})
		route["final"] = config.TagDirect
	} else {
		outbounds = append(outbounds,
			map[string]interface{}{
				"type": "urltest", "tag": config.TagAuto,
				"outbounds": tags, "url": p.ProbeURL, "interval": p.ProbeInterval,
			},
			map[string]interface{}{
				"type": "selector", "tag": config.TagProxy,
				"outbounds": append([]string{config.TagAuto}, tags...), "default": config.TagAuto,
			},
			map[string]interface{}{"type": "direct", "tag": config.TagDirect},
		)
		route["final"] = p.SingBox.Final
		if len(p.SingBox.Rules) > 0 {
			route["rules"] = p.SingBox.Rules
		}
		if len(p.SingBox.RuleSets) > 0 {
			route["rule_set"] = p.SingBox.RuleSets
		}
	}

	doc := map[string]interface{}{
		"log":       map[string]interface{}{"level": "warn"},
		"outbounds": outbounds,
		"route":     route,
	}
	if len(tags) > 0 && len(p.SingBox.DNS) > 0 {
		doc["dns"] = json.RawMessage(p.SingBox.DNS)
	}
	return json.MarshalIndent(doc, "", "  ")
}

// singBoxProxyTags extracts the node transport tags (skipping any injected
// direct/proxy/auto entries — defensive, contracts emit only node transports).
func singBoxProxyTags(outbounds []map[string]interface{}) []string {
	reserved := map[string]bool{config.TagDirect: true, config.TagProxy: true, config.TagAuto: true}
	var tags []string
	for _, o := range outbounds {
		tag, _ := o["tag"].(string)
		if tag == "" || reserved[tag] {
			continue
		}
		tags = append(tags, tag)
	}
	return tags
}

// clashDocument wraps the frozen contract `proxies:` block with proxy-groups
// (select + url-test for the fallback cascade), the split-tunnel rules and
// rule-providers/dns from policy. Map/list fragments are emitted as JSON, which
// is valid YAML flow syntax — keeping the service stdlib-only like contracts.
func clashDocument(b contracts.SubscriptionBundle, p config.RoutingPolicy) ([]byte, error) {
	var sb strings.Builder
	sb.Write(b.ToClashMetaYAML()) // "proxies:\n  - ...\n"

	names := clashProxyNames(b)
	if len(names) > 0 {
		groups := []map[string]interface{}{
			{"name": config.ClashProxyGroup, "type": "select",
				"proxies": append([]string{config.ClashAutoGroup}, names...)},
			{"name": config.ClashAutoGroup, "type": "url-test",
				"url": p.ProbeURL, "interval": intervalSeconds(p.ProbeInterval), "proxies": names},
		}
		gj, err := json.Marshal(groups)
		if err != nil {
			return nil, err
		}
		sb.WriteString("proxy-groups: ")
		sb.Write(gj)
		sb.WriteByte('\n')
	}

	if len(p.Clash.RuleProviders) > 0 {
		rj, err := json.Marshal(p.Clash.RuleProviders)
		if err != nil {
			return nil, err
		}
		sb.WriteString("rule-providers: ")
		sb.Write(rj)
		sb.WriteByte('\n')
	}
	if len(p.Clash.DNS) > 0 {
		dj, err := json.Marshal(p.Clash.DNS)
		if err != nil {
			return nil, err
		}
		sb.WriteString("dns: ")
		sb.Write(dj)
		sb.WriteByte('\n')
	}

	sb.WriteString("rules:\n")
	final := "DIRECT"
	if len(names) > 0 {
		final = config.ClashProxyGroup
	}
	for _, r := range p.Clash.Rules {
		sb.WriteString("  - ")
		sb.WriteString(r)
		sb.WriteByte('\n')
	}
	sb.WriteString("  - MATCH,")
	sb.WriteString(final)
	sb.WriteByte('\n')
	return []byte(sb.String()), nil
}

// clashProxyNames collects proxy names in the SAME order ToClashMetaYAML emits
// them (node order, then enabled transports within a node).
func clashProxyNames(b contracts.SubscriptionBundle) []string {
	var names []string
	for _, n := range b.Nodes {
		for _, t := range n.Transports {
			if t.Enabled {
				names = append(names, t.Tag)
			}
		}
	}
	return names
}

// intervalSeconds converts a Go duration string ("3m") to whole seconds for
// Clash's numeric interval. Falls back to 180s on parse failure.
func intervalSeconds(d string) int {
	dur, err := time.ParseDuration(d)
	if err != nil || dur <= 0 {
		return 180
	}
	return int(dur / time.Second)
}

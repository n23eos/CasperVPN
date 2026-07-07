package contracts

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file defines the subscription OUTPUT format: one canonical node set
// rendered three ways, so any client can consume it —
//
//	1. Base64 URI list  — the classic sub format (Happ, v2rayN, sing-box import).
//	2. sing-box JSON    — native sing-box outbounds.
//	3. Clash-Meta YAML  — proxies list for Clash.Meta / mihomo.
//
// All three describe the SAME set. The renderer stamps each user's PERSONAL
// secrets (UUID, RealityShortID) into every proxy, so the output is per-user.
//
// Coverage note (frozen decision): AmneziaWG has no portable URI scheme, so it
// is OMITTED from the base64 URI list but PRESENT in the sing-box JSON and
// Clash-Meta YAML representations (both model WireGuard/Amnezia natively). The
// base64 list carries vless-reality, hysteria2 and shadowsocks-2022. See
// docs/contracts.md.

// SubscriptionBundle is the canonical input to every renderer: the user whose
// personal secrets get stamped in, and the nodes to expose.
type SubscriptionBundle struct {
	User        User      `json:"user"`
	Nodes       []Node    `json:"nodes"`
	GeneratedAt time.Time `json:"generated_at"`
}

// proxyAddr returns the address clients dial for a node (currently EntryIP).
func proxyAddr(n Node) string { return n.EntryIP }

// enabledTransports returns a node's enabled transports in priority order is the
// caller's concern; here we simply filter disabled ones out.
func enabledTransports(n Node) []Transport {
	out := make([]Transport, 0, len(n.Transports))
	for _, t := range n.Transports {
		if t.Enabled {
			out = append(out, t)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 1. Base64 URI list
// ---------------------------------------------------------------------------

// ToBase64List renders the bundle as a newline-joined list of share URIs, then
// standard-base64 encodes the whole blob (the wire form clients expect).
// AmneziaWG transports are skipped (no portable URI scheme).
func (b SubscriptionBundle) ToBase64List() string {
	var lines []string
	for _, n := range b.Nodes {
		addr := proxyAddr(n)
		for _, t := range enabledTransports(n) {
			if uri := b.transportURI(n, t, addr); uri != "" {
				lines = append(lines, uri)
			}
		}
	}
	blob := strings.Join(lines, "\n")
	return base64.StdEncoding.EncodeToString([]byte(blob))
}

// transportURI builds one share URI for a transport, or "" if unrepresentable.
func (b SubscriptionBundle) transportURI(n Node, t Transport, addr string) string {
	tag := url.PathEscape(t.Tag)
	switch t.Type {
	case TransportVlessReality:
		p := t.VlessReality
		if p == nil {
			return ""
		}
		sni := ""
		if len(p.ServerNames) > 0 {
			sni = p.ServerNames[0]
		}
		q := url.Values{}
		q.Set("encryption", "none")
		q.Set("security", "reality")
		q.Set("sni", sni)
		q.Set("pbk", p.PublicKey)
		q.Set("sid", b.User.RealityShortID) // per-user short-id
		q.Set("flow", p.Flow)
		q.Set("type", "tcp")
		if p.Fingerprint != "" {
			q.Set("fp", p.Fingerprint)
		}
		return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
			b.User.UUID, addr, t.Port, q.Encode(), tag)

	case TransportHysteria2:
		p := t.Hysteria2
		if p == nil {
			return ""
		}
		q := url.Values{}
		q.Set("sni", p.SNI)
		if p.Obfs != "" {
			q.Set("obfs", p.Obfs)
			q.Set("obfs-password", p.ObfsPassword)
		}
		q.Set("insecure", boolQ(p.Insecure))
		return fmt.Sprintf("hysteria2://%s@%s:%d?%s#%s",
			url.QueryEscape(p.Password), addr, t.Port, q.Encode(), tag)

	case TransportShadowsocks2022:
		p := t.Shadowsocks2022
		if p == nil {
			return ""
		}
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(p.Method + ":" + p.PSK))
		return fmt.Sprintf("ss://%s@%s:%d#%s", userinfo, addr, t.Port, tag)

	case TransportAmneziaWG:
		// Intentionally omitted from the URI list (no portable scheme).
		return ""
	}
	return ""
}

func boolQ(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// ---------------------------------------------------------------------------
// 2. sing-box JSON
// ---------------------------------------------------------------------------

// SingBoxConfig is the minimal sing-box document this contract emits: a set of
// outbounds. Fields beyond outbounds (dns, route, inbounds) are the client's to
// add. Outbounds are untyped maps because sing-box's per-type schema is large
// and version-sensitive; the shapes here follow current sing-box conventions.
type SingBoxConfig struct {
	Outbounds []map[string]interface{} `json:"outbounds"`
}

// ToSingBox builds the sing-box outbound set for the bundle (all transports).
func (b SubscriptionBundle) ToSingBox() SingBoxConfig {
	var outs []map[string]interface{}
	for _, n := range b.Nodes {
		addr := proxyAddr(n)
		for _, t := range enabledTransports(n) {
			if o := b.singBoxOutbound(n, t, addr); o != nil {
				outs = append(outs, o)
			}
		}
	}
	return SingBoxConfig{Outbounds: outs}
}

// ToSingBoxJSON marshals ToSingBox to indented JSON.
func (b SubscriptionBundle) ToSingBoxJSON() ([]byte, error) {
	return json.MarshalIndent(b.ToSingBox(), "", "  ")
}

func (b SubscriptionBundle) singBoxOutbound(n Node, t Transport, addr string) map[string]interface{} {
	switch t.Type {
	case TransportVlessReality:
		p := t.VlessReality
		if p == nil {
			return nil
		}
		sni := ""
		if len(p.ServerNames) > 0 {
			sni = p.ServerNames[0]
		}
		return map[string]interface{}{
			"type": "vless", "tag": t.Tag, "server": addr, "server_port": t.Port,
			"uuid": b.User.UUID, "flow": p.Flow,
			"tls": map[string]interface{}{
				"enabled": true, "server_name": sni,
				"utls":    map[string]interface{}{"enabled": p.Fingerprint != "", "fingerprint": p.Fingerprint},
				"reality": map[string]interface{}{"enabled": true, "public_key": p.PublicKey, "short_id": b.User.RealityShortID},
			},
		}
	case TransportHysteria2:
		p := t.Hysteria2
		if p == nil {
			return nil
		}
		o := map[string]interface{}{
			"type": "hysteria2", "tag": t.Tag, "server": addr, "server_port": t.Port,
			"password": p.Password,
			"tls":      map[string]interface{}{"enabled": true, "server_name": p.SNI, "insecure": p.Insecure},
		}
		if p.Obfs != "" {
			o["obfs"] = map[string]interface{}{"type": p.Obfs, "password": p.ObfsPassword}
		}
		return o
	case TransportShadowsocks2022:
		p := t.Shadowsocks2022
		if p == nil {
			return nil
		}
		return map[string]interface{}{
			"type": "shadowsocks", "tag": t.Tag, "server": addr, "server_port": t.Port,
			"method": p.Method, "password": p.PSK,
		}
	case TransportAmneziaWG:
		p := t.AmneziaWG
		if p == nil {
			return nil
		}
		// Modeled as a wireguard outbound plus Amnezia obfuscation knobs.
		return map[string]interface{}{
			"type": "wireguard", "tag": t.Tag, "server": addr, "server_port": t.Port,
			"peer_public_key": p.PublicKey, "mtu": p.MTU,
			"jc": p.Jc, "jmin": p.Jmin, "jmax": p.Jmax, "s1": p.S1, "s2": p.S2,
			"h1": p.H1, "h2": p.H2, "h3": p.H3, "h4": p.H4,
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 3. Clash-Meta YAML
// ---------------------------------------------------------------------------

// ToClashMetaYAML renders a Clash.Meta / mihomo `proxies:` list (all
// transports). Emitted by hand (stdlib only) to avoid a YAML dependency in the
// frozen contracts package.
func (b SubscriptionBundle) ToClashMetaYAML() []byte {
	var sb strings.Builder
	sb.WriteString("proxies:\n")
	for _, n := range b.Nodes {
		addr := proxyAddr(n)
		for _, t := range enabledTransports(n) {
			b.clashProxy(&sb, t, addr)
		}
	}
	return []byte(sb.String())
}

// clashProxy appends one `- name: ...` block for a transport.
func (b SubscriptionBundle) clashProxy(sb *strings.Builder, t Transport, addr string) {
	line := func(indent, k, v string) {
		sb.WriteString(indent)
		sb.WriteString(k)
		sb.WriteString(": ")
		sb.WriteString(v)
		sb.WriteString("\n")
	}
	switch t.Type {
	case TransportVlessReality:
		p := t.VlessReality
		if p == nil {
			return
		}
		sni := ""
		if len(p.ServerNames) > 0 {
			sni = p.ServerNames[0]
		}
		line("  - ", "name", yq(t.Tag))
		line("    ", "type", "vless")
		line("    ", "server", yq(addr))
		line("    ", "port", strconv.Itoa(t.Port))
		line("    ", "uuid", yq(b.User.UUID))
		line("    ", "flow", yq(p.Flow))
		line("    ", "tls", "true")
		line("    ", "servername", yq(sni))
		if p.Fingerprint != "" {
			line("    ", "client-fingerprint", yq(p.Fingerprint))
		}
		sb.WriteString("    reality-opts:\n")
		line("      ", "public-key", yq(p.PublicKey))
		line("      ", "short-id", yq(b.User.RealityShortID))
	case TransportHysteria2:
		p := t.Hysteria2
		if p == nil {
			return
		}
		line("  - ", "name", yq(t.Tag))
		line("    ", "type", "hysteria2")
		line("    ", "server", yq(addr))
		line("    ", "port", strconv.Itoa(t.Port))
		line("    ", "password", yq(p.Password))
		line("    ", "sni", yq(p.SNI))
		if p.Obfs != "" {
			line("    ", "obfs", yq(p.Obfs))
			line("    ", "obfs-password", yq(p.ObfsPassword))
		}
		line("    ", "skip-cert-verify", strconv.FormatBool(p.Insecure))
	case TransportShadowsocks2022:
		p := t.Shadowsocks2022
		if p == nil {
			return
		}
		line("  - ", "name", yq(t.Tag))
		line("    ", "type", "ss")
		line("    ", "server", yq(addr))
		line("    ", "port", strconv.Itoa(t.Port))
		line("    ", "cipher", yq(p.Method))
		line("    ", "password", yq(p.PSK))
	case TransportAmneziaWG:
		p := t.AmneziaWG
		if p == nil {
			return
		}
		line("  - ", "name", yq(t.Tag))
		line("    ", "type", "wireguard")
		line("    ", "server", yq(addr))
		line("    ", "port", strconv.Itoa(t.Port))
		line("    ", "public-key", yq(p.PublicKey))
		if p.MTU > 0 {
			line("    ", "mtu", strconv.Itoa(p.MTU))
		}
		// Amnezia obfuscation knobs (mihomo amnezia-wg-option).
		sb.WriteString("    amnezia-wg-option:\n")
		line("      ", "jc", strconv.Itoa(p.Jc))
		line("      ", "jmin", strconv.Itoa(p.Jmin))
		line("      ", "jmax", strconv.Itoa(p.Jmax))
		line("      ", "s1", strconv.Itoa(p.S1))
		line("      ", "s2", strconv.Itoa(p.S2))
		line("      ", "h1", strconv.FormatInt(p.H1, 10))
		line("      ", "h2", strconv.FormatInt(p.H2, 10))
		line("      ", "h3", strconv.FormatInt(p.H3, 10))
		line("      ", "h4", strconv.FormatInt(p.H4, 10))
	}
}

// yq double-quotes a YAML scalar and escapes the minimal set (backslash, quote).
func yq(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// Package render negotiates the output format and turns a personalized bundle
// into the wire payload. Base64 comes straight from the frozen contract
// renderer; sing-box JSON and Clash YAML wrap the contract's proxy set with the
// split-tunnel routing policy (which the frozen SingBoxConfig deliberately omits).
package render

import "strings"

// Format is one of the three subscription representations.
type Format string

const (
	FormatBase64  Format = "base64"
	FormatSingBox Format = "singbox"
	FormatClash   Format = "clash"
)

// Negotiate picks a format: explicit ?format wins, then Accept, then User-Agent,
// else base64 (the universal default Happ/v2rayN import directly).
func Negotiate(formatParam, accept, userAgent string) Format {
	switch strings.ToLower(strings.TrimSpace(formatParam)) {
	case "base64":
		return FormatBase64
	case "singbox", "sing-box":
		return FormatSingBox
	case "clash", "clash-meta", "mihomo":
		return FormatClash
	}

	switch a := strings.ToLower(accept); {
	case strings.Contains(a, "application/json"):
		return FormatSingBox
	case strings.Contains(a, "application/yaml"), strings.Contains(a, "text/yaml"), strings.Contains(a, "application/x-yaml"):
		return FormatClash
	case strings.Contains(a, "text/plain"):
		return FormatBase64
	}

	ua := strings.ToLower(userAgent)
	switch {
	case strings.Contains(ua, "clash"), strings.Contains(ua, "mihomo"), strings.Contains(ua, "meta"):
		return FormatClash
	case strings.Contains(ua, "sing-box"), strings.Contains(ua, "singbox"):
		return FormatSingBox
	case strings.Contains(ua, "happ"), strings.Contains(ua, "v2ray"), strings.Contains(ua, "nekobox"),
		strings.Contains(ua, "streisand"), strings.Contains(ua, "shadowrocket"):
		return FormatBase64
	}
	return FormatBase64
}

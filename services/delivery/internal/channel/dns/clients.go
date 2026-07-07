package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultHTTPClient is the bounded-timeout fallback for DoH when no client is
// injected, so a stalled resolver can't hang the lookup (http.DefaultClient has
// no timeout).
var defaultHTTPClient = &http.Client{Timeout: 10 * time.Second}

// Do53Resolver adapts net.Resolver to the Lookup interface (plain UDP/TCP DNS).
// The system or a config-supplied resolver answers; nothing is hardcoded here.
type Do53Resolver struct {
	R *net.Resolver // nil => net.DefaultResolver
}

// LookupTXT resolves TXT strings over plain DNS.
func (d Do53Resolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	r := d.R
	if r == nil {
		r = net.DefaultResolver
	}
	return r.LookupTXT(ctx, name)
}

// httpDoer is the slice of *http.Client this package needs, so tests can inject a
// fake transport without a real network.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// DoHClient resolves TXT records over DNS-over-HTTPS using the JSON API shape
// (application/dns-json, as served by major public resolvers). The endpoint is
// config-supplied; requests look like ordinary DoH traffic to the network.
type DoHClient struct {
	Endpoint string   // e.g. https://<resolver>/dns-query — from config, never hardcoded
	HTTP     httpDoer // nil => http.DefaultClient
}

// dohAnswer is the subset of the DoH JSON response we consume.
type dohResponse struct {
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

// LookupTXT performs a DoH JSON query for TXT records and returns their strings.
func (c DoHClient) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if c.Endpoint == "" {
		return nil, fmt.Errorf("dns: DoH endpoint required")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("dns: bad DoH endpoint: %w", err)
	}
	q := u.Query()
	q.Set("name", name)
	q.Set("type", "TXT")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")

	doer := c.HTTP
	if doer == nil {
		doer = defaultHTTPClient
	}
	resp, err := doer.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dns: DoH status %d", resp.StatusCode)
	}

	var dr dohResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return nil, fmt.Errorf("dns: decode DoH response: %w", err)
	}
	const txtType = 16 // RFC 1035 TXT
	var out []string
	for _, a := range dr.Answer {
		if a.Type != txtType {
			continue
		}
		// DoH JSON returns TXT data quoted, e.g. "\"abc\" \"def\""; unwrap each.
		out = append(out, unquoteTXT(a.Data)...)
	}
	return out, nil
}

// unquoteTXT splits a DoH TXT "data" field into its constituent character-strings,
// stripping the surrounding double quotes each carries.
func unquoteTXT(data string) []string {
	fields := strings.Fields(data)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimPrefix(f, "\"")
		f = strings.TrimSuffix(f, "\"")
		out = append(out, f)
	}
	if len(out) == 0 {
		return []string{data}
	}
	return out
}

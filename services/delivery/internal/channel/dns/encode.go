// Package dns delivers the signed blob through DNS: the bytes are base32-encoded
// into TXT record strings, retrievable over plain DNS (Do53) or DNS-over-HTTPS.
// To a network observer the traffic is an ordinary TXT lookup — the covert-ness
// is that it looks exactly like normal DNS, not that it is "more encrypted".
//
// This file is the medium-neutral codec + name derivation shared by the DoH and
// the plain-TXT channels.
package dns

import (
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
)

// b32 is lowercase, unpadded base32 — DNS is case-insensitive and padding chars
// ('=') are not valid in TXT-carried tokens, so we strip them.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// txtStringMax is the maximum length of a single TXT character-string (RFC 1035
// caps it at 255 octets). A long blob is split across several strings which the
// resolver returns together and we re-join.
const txtStringMax = 255

// labelMax is the DNS label length cap (RFC 1035): 63 octets per dot-separated
// label. Encoded key labels are chunked to respect it.
const labelMax = 63

// EncodeTXT encodes a blob into one TXT record's character-strings.
func EncodeTXT(blob []byte) []string {
	s := strings.ToLower(b32.EncodeToString(blob))
	var out []string
	for len(s) > txtStringMax {
		out = append(out, s[:txtStringMax])
		s = s[txtStringMax:]
	}
	return append(out, s)
}

// DecodeTXT reverses EncodeTXT: join the strings, base32-decode.
func DecodeTXT(strs []string) ([]byte, error) {
	joined := strings.ToUpper(strings.Join(strs, ""))
	if joined == "" {
		return nil, errors.New("dns: empty TXT record")
	}
	b, err := b32.DecodeString(joined)
	if err != nil {
		return nil, fmt.Errorf("dns: bad base32 TXT payload: %w", err)
	}
	return b, nil
}

// QName derives the fully-qualified query name for a delivery key under a zone.
// The key is base32-encoded (DNS-safe) and split into <=63-octet labels, so it
// reads like a normal subdomain lookup. zone is config-supplied, never hardcoded.
func QName(zone, key string) (string, error) {
	if zone == "" {
		return "", errors.New("dns: zone required")
	}
	// Normalize the zone to a bare form (no leading/trailing dots); we emit a
	// name without a trailing root dot — lookups accept either form.
	zone = strings.Trim(zone, ".")
	if zone == "" {
		return "", errors.New("dns: zone required")
	}
	enc := strings.ToLower(b32.EncodeToString([]byte(key)))
	var labels []string
	for len(enc) > labelMax {
		labels = append(labels, enc[:labelMax])
		enc = enc[labelMax:]
	}
	labels = append(labels, enc)
	return strings.Join(labels, ".") + "." + zone, nil
}

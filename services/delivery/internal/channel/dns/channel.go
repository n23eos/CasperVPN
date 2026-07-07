package dns

import (
	"context"
	"errors"
	"fmt"

	"github.com/caspervpn/delivery/internal/channel"
)

// Lookup abstracts "resolve TXT strings for a name". Two implementations back the
// two channels: a plain Do53 resolver (net.Resolver.LookupTXT) and a DoH client
// (an HTTPS request to a resolver's JSON/DoH endpoint). Both are injected — no
// resolver address or DoH URL is hardcoded (CLAUDE.md [ANTI-BLOCK] rule 5).
type Lookup interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Publisher abstracts "set the TXT strings for a name" via a DNS provider API.
// Optional: a fetch-only channel (Publisher == nil) reads records populated out
// of band. Publishing is provider-side and identical for Do53 and DoH readers.
type Publisher interface {
	SetTXT(ctx context.Context, name string, strs []string) error
}

// Channel is a DNS delivery channel. kind distinguishes the DoH reader from the
// plain-TXT reader; both share this type since only the Lookup differs.
type Channel struct {
	kind channel.Kind
	zone string
	look Lookup
	pub  Publisher // may be nil (fetch-only)
}

// NewDoH builds a DNS-over-HTTPS delivery channel. look is a DoH client.
func NewDoH(zone string, look Lookup, pub Publisher) (*Channel, error) {
	return newChannel(channel.KindDoH, zone, look, pub)
}

// NewTXT builds a plain-DNS (Do53) TXT delivery channel. look is a Do53 resolver.
func NewTXT(zone string, look Lookup, pub Publisher) (*Channel, error) {
	return newChannel(channel.KindDNSTXT, zone, look, pub)
}

func newChannel(kind channel.Kind, zone string, look Lookup, pub Publisher) (*Channel, error) {
	if zone == "" {
		return nil, errors.New("dns: zone required")
	}
	if look == nil {
		return nil, errors.New("dns: lookup required")
	}
	return &Channel{kind: kind, zone: zone, look: look, pub: pub}, nil
}

// Kind identifies this channel (doh or dns_txt).
func (c *Channel) Kind() channel.Kind { return c.kind }

// Publish sets the blob as TXT strings for the key's derived name.
func (c *Channel) Publish(ctx context.Context, key string, blob []byte) error {
	if c.pub == nil {
		return errors.New("dns: channel is fetch-only (no publisher configured)")
	}
	name, err := QName(c.zone, key)
	if err != nil {
		return err
	}
	return c.pub.SetTXT(ctx, name, EncodeTXT(blob))
}

// Fetch resolves the key's TXT record and decodes it back to the blob.
func (c *Channel) Fetch(ctx context.Context, key string) ([]byte, error) {
	name, err := QName(c.zone, key)
	if err != nil {
		return nil, err
	}
	strs, err := c.look.LookupTXT(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", channel.ErrNotFound, err)
	}
	if len(strs) == 0 {
		return nil, channel.ErrNotFound
	}
	blob, err := DecodeTXT(strs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", channel.ErrNotFound, err)
	}
	return blob, nil
}

// Healthy probes the zone apex for any TXT answer; a lookup that returns without a
// transport error means the resolver path is working.
func (c *Channel) Healthy(ctx context.Context) bool {
	_, err := c.look.LookupTXT(ctx, "health."+c.zone)
	return err == nil
}

// Package channel defines the medium-independent contract every delivery channel
// implements, plus a registry and a resolver that fails over across channels.
//
// The whole anti-block premise lives here: the SAME signed blob is publishable to
// many channels of DIFFERENT nature (messenger, DNS, git-raw, steganographic), and
// a client tries them in turn until one returns bytes that pass signature
// verification. No single channel is a point of failure.
package channel

import (
	"context"
	"errors"
)

// Kind names a channel's medium. The first five mirror contracts.DeliveryChannel
// wire values; "steg" is delivery-internal (no contract enum value, since it is a
// covert carrier, not an addressable API surface).
type Kind string

const (
	KindHTTPS     Kind = "https"
	KindDoH       Kind = "doh"
	KindDNSTXT    Kind = "dns_txt"
	KindTelegram  Kind = "telegram"
	KindGitHubRaw Kind = "github_raw"
	KindSteg      Kind = "steg"
)

// Channel moves opaque signed blobs over one medium. It never inspects, verifies
// or decrypts what it carries — that is the artifact/pointer layer's job.
type Channel interface {
	// Kind identifies the medium (for routing, health and telemetry).
	Kind() Kind

	// Publish stores blob under key on the channel's backing medium (server side:
	// post to the bot store, write the git-raw file, set the TXT record, ...).
	Publish(ctx context.Context, key string, blob []byte) error

	// Fetch retrieves the raw blob stored under key (client side). It returns
	// ErrNotFound when the key is absent so the resolver can move on.
	Fetch(ctx context.Context, key string) ([]byte, error)

	// Healthy reports a cheap liveness signal, used to skip known-dead channels.
	Healthy(ctx context.Context) bool
}

// ErrNotFound is returned by Fetch when a key is not present on a channel.
var ErrNotFound = errors.New("channel: key not found")

// ErrUnavailable is returned by Fetch when a channel is reachable-but-degraded or
// the external medium failed transiently (distinct from a missing key).
var ErrUnavailable = errors.New("channel: temporarily unavailable")

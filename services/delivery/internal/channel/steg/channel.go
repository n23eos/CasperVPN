package steg

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/caspervpn/delivery/internal/channel"
)

// Carrier is the innocuous-looking external surface the cover documents live on
// (e.g. an analytics-ingest endpoint that echoes the last batch, or a paste-like
// store). Injected so no host is hardcoded and tests use an in-memory fake.
type Carrier interface {
	// PutCover stores a cover document under key (server side).
	PutCover(ctx context.Context, key string, cover []byte) error
	// GetCover retrieves the cover document for key (client side).
	GetCover(ctx context.Context, key string) ([]byte, error)
}

// Channel hides the signed blob inside a cover document on a Carrier.
type Channel struct {
	carrier Carrier
}

// New builds a steganographic channel over a carrier.
func New(carrier Carrier) (*Channel, error) {
	if carrier == nil {
		return nil, errors.New("steg: carrier required")
	}
	return &Channel{carrier: carrier}, nil
}

// Kind identifies this channel.
func (c *Channel) Kind() channel.Kind { return channel.KindSteg }

// Publish embeds blob into a fresh cover document and stores it.
func (c *Channel) Publish(ctx context.Context, key string, blob []byte) error {
	cover, err := Embed(blob, randomAnonID())
	if err != nil {
		return err
	}
	return c.carrier.PutCover(ctx, key, cover)
}

// Fetch retrieves the cover document and extracts the hidden blob.
func (c *Channel) Fetch(ctx context.Context, key string) ([]byte, error) {
	cover, err := c.carrier.GetCover(ctx, key)
	if err != nil {
		return nil, err
	}
	blob, err := Extract(cover)
	if err != nil {
		return nil, err
	}
	return blob, nil
}

// Healthy reports whether the carrier answers a probe key.
func (c *Channel) Healthy(ctx context.Context) bool {
	_, err := c.carrier.GetCover(ctx, "health")
	return err == nil || errors.Is(err, channel.ErrNotFound)
}

// randomAnonID produces an opaque, non-covert marker so each cover looks like a
// distinct client session. Failure to read randomness is non-fatal (cosmetic).
func randomAnonID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

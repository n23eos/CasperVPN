package telegram

import (
	"context"
	"errors"

	"github.com/caspervpn/delivery/internal/channel"
)

// BlobStore is where the messenger channel keeps signed blobs it serves as a
// generic delivery channel (e.g. the signed directory, posted to a bot channel or
// pinned message a client reads back). Injected so production can back it with a
// real bot-channel fetch and tests use an in-memory fake.
type BlobStore interface {
	Put(ctx context.Context, key string, blob []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// Channel adapts the messenger to the generic channel.Channel contract, letting
// the resolver treat the bot as one more redundant carrier for the signed
// artifact alongside DNS, git-raw and steg.
type Channel struct {
	store BlobStore
}

// NewChannel builds the messenger delivery channel over a blob store.
func NewChannel(store BlobStore) (*Channel, error) {
	if store == nil {
		return nil, errors.New("telegram: blob store required")
	}
	return &Channel{store: store}, nil
}

// Kind identifies this channel.
func (c *Channel) Kind() channel.Kind { return channel.KindTelegram }

// Publish stores the blob for later retrieval by clients over the messenger.
func (c *Channel) Publish(ctx context.Context, key string, blob []byte) error {
	return c.store.Put(ctx, key, blob)
}

// Fetch retrieves the blob a client requested over the messenger.
func (c *Channel) Fetch(ctx context.Context, key string) ([]byte, error) {
	return c.store.Get(ctx, key)
}

// Healthy reports whether the store answers a probe.
func (c *Channel) Healthy(ctx context.Context) bool {
	_, err := c.store.Get(ctx, "health")
	return err == nil || errors.Is(err, channel.ErrNotFound)
}

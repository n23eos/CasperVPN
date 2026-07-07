// Package gitraw delivers the signed blob as a file served from git-raw mirrors
// (GitHub raw, GitLab raw, or any raw-file host). The blob is signed AND (for
// directories) sealed, so a raw file sitting in a public repo leaks nothing and
// cannot be substituted without breaking the signature.
//
// Resilience comes from MIRROR ROTATION: the same file lives on several raw hosts;
// the client round-robins and tries the next mirror when one is blocked or 404s.
// Mirror bases are config-supplied and updatable — never hardcoded.
package gitraw

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/caspervpn/delivery/internal/channel"
)

// RawTransport fetches a path from ONE mirror base. The real implementation does
// an HTTPS GET of base+"/"+path; tests inject a fake. Kept as an interface so no
// network or hardcoded host leaks into this package.
type RawTransport interface {
	Get(ctx context.Context, base, path string) ([]byte, error)
}

// RawWriter publishes a file to the origin the mirrors replicate (e.g. a git
// commit via API). Optional: if nil, the channel is fetch-only (mirrors are
// populated out of band). Real impls live in infra; tests inject a fake.
type RawWriter interface {
	Put(ctx context.Context, path string, blob []byte) error
}

// Channel is a git-raw delivery channel over a rotating set of mirrors.
type Channel struct {
	mirrors []string // raw base URLs, config-supplied, priority by slice order
	tr      RawTransport
	w       RawWriter // may be nil (fetch-only)
	cursor  uint32    // round-robin start index across Fetch calls
}

// New builds a git-raw channel. mirrors must be non-empty; tr is required.
func New(mirrors []string, tr RawTransport, w RawWriter) (*Channel, error) {
	if len(mirrors) == 0 {
		return nil, errors.New("gitraw: at least one mirror required")
	}
	if tr == nil {
		return nil, errors.New("gitraw: transport required")
	}
	// Copy to avoid aliasing caller's slice (immutability at the boundary).
	m := make([]string, len(mirrors))
	copy(m, mirrors)
	return &Channel{mirrors: m, tr: tr, w: w}, nil
}

// Kind identifies this channel.
func (c *Channel) Kind() channel.Kind { return channel.KindGitHubRaw }

// path maps a delivery key to a raw file path. Base64url-in-a-.json name keeps it
// looking like an ordinary asset file in a repo.
func path(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key)) + ".json"
}

// Publish writes the blob (base64-wrapped, so the raw file is valid UTF-8 text) to
// the origin. Fetch-only channels (w == nil) return an error.
func (c *Channel) Publish(ctx context.Context, key string, blob []byte) error {
	if c.w == nil {
		return errors.New("gitraw: channel is fetch-only (no writer configured)")
	}
	enc := []byte(base64.StdEncoding.EncodeToString(blob))
	return c.w.Put(ctx, path(key), enc)
}

// Fetch round-robins the mirrors starting at a rotating cursor, returning the
// first mirror's file that decodes. A blocked or missing mirror is skipped.
func (c *Channel) Fetch(ctx context.Context, key string) ([]byte, error) {
	p := path(key)
	start := int(atomic.AddUint32(&c.cursor, 1)-1) % len(c.mirrors)
	var lastErr error
	for i := 0; i < len(c.mirrors); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		base := c.mirrors[(start+i)%len(c.mirrors)]
		enc, err := c.tr.Get(ctx, base, p)
		if err != nil {
			lastErr = err
			continue
		}
		blob, err := base64.StdEncoding.DecodeString(string(enc))
		if err != nil {
			lastErr = fmt.Errorf("gitraw: mirror %s bad encoding: %w", base, err)
			continue
		}
		return blob, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", channel.ErrNotFound, lastErr)
	}
	return nil, channel.ErrNotFound
}

// Healthy reports true if at least one mirror answers a HEAD-like probe. Here we
// treat "any mirror reachable" as healthy by attempting the cursor mirror only,
// to keep the probe cheap.
func (c *Channel) Healthy(ctx context.Context) bool {
	base := c.mirrors[int(atomic.LoadUint32(&c.cursor))%len(c.mirrors)]
	_, err := c.tr.Get(ctx, base, "healthz")
	// A 404 still means the host is reachable; only a transport error is unhealthy.
	return err == nil || errors.Is(err, channel.ErrNotFound)
}

// Package bridge sketches the volunteer, Snowflake-style expansion channel: end
// users donate a short-lived rendezvous that hands out the SAME signed directory
// artifact, so the set of delivery points grows faster than a censor can
// enumerate it. This is an INTERFACE STUB — the wire protocol and volunteer
// broker are deliberately left for a later phase (see docs/delivery.md, §Bridges).
//
// The contract is fixed now so the rest of delivery can depend on it:
//   - A bridge only ever transports the already signed+sealed blob. It is
//     UNTRUSTED plumbing: a malicious volunteer can drop or corrupt bytes but can
//     never forge a directory (the signature check at the client rejects it) nor
//     read the pointer (it is sealed).
//   - Volunteers are ephemeral and many; discovery of one bridge must not burn the
//     others (per-bridge tokens, no shared long-lived secret).
package bridge

import (
	"context"
	"errors"

	"github.com/caspervpn/delivery/internal/channel"
)

// Offer is a volunteer's advertised rendezvous. Fields are intentionally minimal
// and non-identifying; a real implementation adds NAT-traversal/rendezvous data.
type Offer struct {
	// BridgeID is an opaque, per-bridge token. Burning one does not affect others.
	BridgeID string
	// Hint is optional coarse routing metadata (e.g. region), never PII.
	Hint string
}

// Broker matches clients to volunteer bridges. This is the Snowflake "broker"
// role. STUB: methods return ErrNotImplemented until the volunteer network lands.
type Broker interface {
	// Announce registers a volunteer's Offer (volunteer side).
	Announce(ctx context.Context, o Offer) error
	// Match returns a bridge Offer for a client to try (client side).
	Match(ctx context.Context) (Offer, error)
}

// ErrNotImplemented marks the volunteer network as a future phase.
var ErrNotImplemented = errors.New("bridge: volunteer network not implemented yet")

// StubBroker is a compile-time-complete placeholder so wiring and tests can refer
// to the Broker contract before the real broker exists. Every method is a no-op
// that reports ErrNotImplemented — it never silently pretends to work.
type StubBroker struct{}

// Announce is not yet implemented.
func (StubBroker) Announce(context.Context, Offer) error { return ErrNotImplemented }

// Match is not yet implemented.
func (StubBroker) Match(context.Context) (Offer, error) { return Offer{}, ErrNotImplemented }

// Channel is the future bridge-backed delivery channel. It satisfies the
// channel.Channel shape so a working broker can be dropped into the registry
// later with no changes elsewhere. Until then Publish/Fetch report the stub error.
type Channel struct {
	broker Broker
}

// NewChannel builds a bridge channel over a broker (StubBroker for now).
func NewChannel(b Broker) *Channel { return &Channel{broker: b} }

// Kind reuses the steg-adjacent volunteer identity. Bridges are their own nature
// but share the "covert expansion" role; a dedicated Kind can be added when the
// contract enum gains one. For now it is tagged as a distinct internal kind.
func (c *Channel) Kind() channel.Kind { return kindBridge }

const kindBridge channel.Kind = "bridge"

// Publish is not yet implemented (no volunteer to publish through).
func (c *Channel) Publish(context.Context, string, []byte) error { return ErrNotImplemented }

// Fetch is not yet implemented (no volunteer network to fetch from).
func (c *Channel) Fetch(context.Context, string) ([]byte, error) { return nil, ErrNotImplemented }

// Healthy is false until a real broker is wired in.
func (c *Channel) Healthy(context.Context) bool { return false }

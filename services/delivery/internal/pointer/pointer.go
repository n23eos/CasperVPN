// Package pointer models the DYNAMIC directory of entry points, mirrors and bot
// handles — the thing public channels distribute in encrypted form. It is a
// "pointer" precisely because it does not contain a user's per-user secret: it
// tells a client WHERE to fetch its real config (and which channels/mirrors to
// try next), and it is sealed before it ever touches a public channel.
//
// Nothing in a Directory is hardcoded in the binary. It is assembled from
// config/DB (CLAUDE.md [ANTI-BLOCK] rule 5) and is refreshable — operators
// republish a new signed+sealed Directory over every channel when entry points
// rotate or a channel is blocked.
package pointer

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/delivery/internal/artifact"
	"github.com/caspervpn/delivery/internal/seal"
)

// EntryPoint is one advertised place a client can reach the service. It carries
// only coarse, non-secret coordinates; the per-user UUID/short-id is fetched from
// SubscriptionBase over an authenticated path, never embedded here.
type EntryPoint struct {
	NodeID           string                    `json:"node_id"`
	Region           string                    `json:"region"`
	Transports       []contracts.TransportType `json:"transports,omitempty"`
	SubscriptionBase string                    `json:"subscription_base"` // config-supplied base to fetch per-user config
}

// Mirror points at one more place to look for the NEXT directory (or config),
// over a specific channel. Clients rotate through mirrors on failure.
type Mirror struct {
	Kind     contracts.DeliveryChannel `json:"kind"`
	Endpoint string                    `json:"endpoint"` // bot handle, DoH URL, raw base, TXT name — config-supplied
	Priority int                       `json:"priority"` // lower = tried earlier
}

// Directory is the full dynamic list. Revision + IssuedAt let a client keep the
// freshest copy and reject stale/rolled-back ones.
type Directory struct {
	Revision    int          `json:"revision"`
	IssuedAt    time.Time    `json:"issued_at"`
	EntryPoints []EntryPoint `json:"entry_points"`
	Mirrors     []Mirror     `json:"mirrors"`
	BotHandles  []string     `json:"bot_handles,omitempty"`
}

// MarshalJSON-style helpers are just encoding/json; kept as methods for symmetry.
func (d Directory) encode() ([]byte, error) { return json.Marshal(d) }

func decodeDirectory(b []byte) (Directory, error) {
	var d Directory
	if err := json.Unmarshal(b, &d); err != nil {
		return Directory{}, fmt.Errorf("pointer: decode directory: %w", err)
	}
	return d, nil
}

// Pack turns a Directory into a channel-ready wire blob:
//
//	directory JSON --seal--> ciphertext --artifact(KindDirectory)--> --sign--> wire
//
// The sealing step is what keeps the pointer secret on public channels; the
// signing step is what stops a censor from substituting a poisoned directory.
func Pack(d Directory, s *seal.Sealer, signer artifact.Signer) ([]byte, error) {
	plain, err := d.encode()
	if err != nil {
		return nil, err
	}
	sealed, err := s.Seal(plain)
	if err != nil {
		return nil, err
	}
	art := artifact.New(artifact.KindDirectory, d.IssuedAt, sealed)
	signed, err := artifact.Sign(art, signer)
	if err != nil {
		return nil, err
	}
	return signed.Marshal()
}

// Unpack reverses Pack for a client: verify signature FIRST, then decrypt. Order
// matters — we never decrypt bytes we have not authenticated.
func Unpack(wire []byte, v artifact.Verifier, s *seal.Sealer) (Directory, error) {
	art, err := artifact.Open(wire, v) // verifies signature or errors
	if err != nil {
		return Directory{}, err
	}
	if art.Kind != artifact.KindDirectory {
		return Directory{}, fmt.Errorf("pointer: expected directory artifact, got %q", art.Kind)
	}
	plain, err := s.Open(art.Payload)
	if err != nil {
		return Directory{}, err
	}
	return decodeDirectory(plain)
}

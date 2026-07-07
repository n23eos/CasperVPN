// Package artifact defines the canonical, medium-independent unit that every
// delivery channel transports: a versioned envelope carrying an opaque payload.
//
// The delivery layer never lets a channel interpret what it carries. A channel's
// only job is "move these bytes"; signing, sealing and meaning live here and in
// the sign/seal packages. That separation is what lets one payload ride a bot, a
// DNS TXT record, a git-raw file or a steganographic carrier unchanged.
package artifact

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Kind tags what an artifact's payload means. Channels ignore this; consumers use
// it to route a recovered payload to the right decoder.
type Kind string

const (
	// KindDirectory is the DYNAMIC list of entry points / mirrors / bot handles,
	// sealed (encrypted) before it is put on a public channel. It carries no user
	// secret — only an encrypted pointer to where the real per-user config lives.
	KindDirectory Kind = "directory"

	// KindSubscription is a rendered per-user subscription payload (base64 URI
	// list, sing-box JSON or Clash-Meta YAML). Delivered over AUTHENTICATED paths
	// only (e.g. the Telegram bot after identifying the user, or /d/{ch}/{token}).
	KindSubscription Kind = "subscription"
)

// Valid reports whether k is a known Kind.
func (k Kind) Valid() bool {
	switch k {
	case KindDirectory, KindSubscription:
		return true
	}
	return false
}

// Version is the artifact envelope schema revision. Bump on a non-additive change
// to the canonical byte layout so verifiers can reject shapes they do not know.
const Version uint8 = 1

// maxPayload bounds a single artifact so a hostile or corrupt channel blob cannot
// force an unbounded allocation on decode.
const maxPayload = 1 << 20 // 1 MiB

// Artifact is the envelope. Payload is opaque bytes — sealed directory pointer or
// a rendered subscription. IssuedAt lets consumers reject stale/replayed copies.
type Artifact struct {
	Version  uint8
	Kind     Kind
	IssuedAt time.Time
	Payload  []byte
}

// New builds an artifact at the current envelope version.
func New(kind Kind, issuedAt time.Time, payload []byte) Artifact {
	return Artifact{Version: Version, Kind: kind, IssuedAt: issuedAt, Payload: payload}
}

// Canonical returns the deterministic byte encoding that is signed and verified.
// A fixed, length-prefixed layout (not JSON map order) guarantees every party
// hashes the exact same bytes:
//
//	version  : 1 byte
//	issuedAt : 8 bytes, int64 unix nanoseconds, big-endian
//	kindLen  : 2 bytes, uint16 big-endian, then kind bytes
//	payload  : 4 bytes uint32 big-endian length, then payload bytes
//
// The signature is computed over exactly these bytes, so any tamper (kind,
// timestamp or payload) invalidates it.
func (a Artifact) Canonical() ([]byte, error) {
	if !a.Kind.Valid() {
		return nil, fmt.Errorf("artifact: unknown kind %q", a.Kind)
	}
	if len(a.Payload) > maxPayload {
		return nil, fmt.Errorf("artifact: payload %d exceeds max %d", len(a.Payload), maxPayload)
	}
	kind := []byte(a.Kind)
	if len(kind) > 1<<16-1 {
		return nil, errors.New("artifact: kind too long")
	}

	out := make([]byte, 0, 1+8+2+len(kind)+4+len(a.Payload))
	out = append(out, a.Version)

	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(a.IssuedAt.UTC().UnixNano()))
	out = append(out, ts[:]...)

	var kl [2]byte
	binary.BigEndian.PutUint16(kl[:], uint16(len(kind)))
	out = append(out, kl[:]...)
	out = append(out, kind...)

	var pl [4]byte
	binary.BigEndian.PutUint32(pl[:], uint32(len(a.Payload)))
	out = append(out, pl[:]...)
	out = append(out, a.Payload...)
	return out, nil
}

// ErrMalformed is returned when raw bytes do not decode to a valid artifact.
var ErrMalformed = errors.New("artifact: malformed canonical bytes")

// Decode parses bytes produced by Canonical back into an Artifact. It validates
// every length against the buffer so a truncated or oversized blob is rejected
// rather than panicking or over-allocating.
func Decode(b []byte) (Artifact, error) {
	if len(b) < 1+8+2 {
		return Artifact{}, ErrMalformed
	}
	off := 0
	version := b[off]
	off++
	if version != Version {
		return Artifact{}, fmt.Errorf("artifact: unsupported version %d", version)
	}

	issued := int64(binary.BigEndian.Uint64(b[off : off+8]))
	off += 8

	kindLen := int(binary.BigEndian.Uint16(b[off : off+2]))
	off += 2
	if off+kindLen+4 > len(b) {
		return Artifact{}, ErrMalformed
	}
	kind := Kind(b[off : off+kindLen])
	off += kindLen

	payloadLen := int(binary.BigEndian.Uint32(b[off : off+4]))
	off += 4
	if payloadLen > maxPayload || off+payloadLen != len(b) {
		return Artifact{}, ErrMalformed
	}
	payload := make([]byte, payloadLen)
	copy(payload, b[off:off+payloadLen])

	a := Artifact{
		Version:  version,
		Kind:     kind,
		IssuedAt: time.Unix(0, issued).UTC(),
		Payload:  payload,
	}
	if !a.Kind.Valid() {
		return Artifact{}, fmt.Errorf("artifact: unknown kind %q", a.Kind)
	}
	return a, nil
}

package artifact

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Signer signs the canonical bytes of an artifact. Implemented by the sign
// package; declared here as a narrow interface so artifact does not import sign
// (avoids an import cycle) and stays testable with a fake.
type Signer interface {
	// KeyID names the key so a rotating verifier can pick the matching public key.
	KeyID() string
	// Sign returns a detached signature over msg.
	Sign(msg []byte) []byte
}

// Verifier checks a detached signature against a keyed public key. A verifier
// holds MANY keys (a keyring) so signing keys can rotate without breaking clients
// that still hold the previous public key.
type Verifier interface {
	Verify(keyID string, msg, sig []byte) bool
}

// wireMagic marks a signed-artifact blob so a channel that receives foreign bytes
// (e.g. a real DNS TXT record) fails fast instead of misparsing.
var wireMagic = [4]byte{'C', 'V', 'D', '1'} // CasperVPN Delivery v1

// Signed is a signature-bearing artifact: the envelope plus the KeyID and detached
// signature over its canonical bytes. This is the ONLY thing a channel transports.
type Signed struct {
	Artifact  Artifact
	KeyID     string
	Signature []byte
}

// Sign produces a Signed by canonicalizing a and signing those exact bytes.
func Sign(a Artifact, s Signer) (Signed, error) {
	msg, err := a.Canonical()
	if err != nil {
		return Signed{}, err
	}
	return Signed{Artifact: a, KeyID: s.KeyID(), Signature: s.Sign(msg)}, nil
}

// Marshal serializes a Signed to its self-contained wire form:
//
//	magic    : 4 bytes "CVD1"
//	keyIDLen : 2 bytes uint16 big-endian, then keyID
//	sigLen   : 2 bytes uint16 big-endian, then signature
//	canonical: the artifact's canonical bytes (remainder)
func (s Signed) Marshal() ([]byte, error) {
	canon, err := s.Artifact.Canonical()
	if err != nil {
		return nil, err
	}
	if len(s.KeyID) > 1<<16-1 || len(s.Signature) > 1<<16-1 {
		return nil, errors.New("artifact: keyid or signature too long")
	}
	out := make([]byte, 0, 4+2+len(s.KeyID)+2+len(s.Signature)+len(canon))
	out = append(out, wireMagic[:]...)

	var kl [2]byte
	binary.BigEndian.PutUint16(kl[:], uint16(len(s.KeyID)))
	out = append(out, kl[:]...)
	out = append(out, s.KeyID...)

	var sl [2]byte
	binary.BigEndian.PutUint16(sl[:], uint16(len(s.Signature)))
	out = append(out, sl[:]...)
	out = append(out, s.Signature...)

	out = append(out, canon...)
	return out, nil
}

// ErrBadSignature is returned by Open when the signature does not verify. Callers
// MUST treat this as a hard failure — an unverifiable artifact is a possible
// spoof and must never be consumed.
var ErrBadSignature = errors.New("artifact: signature verification failed")

// Unmarshal parses a wire blob into a Signed WITHOUT verifying it. Prefer Open,
// which verifies; Unmarshal exists for tooling that inspects blobs.
func Unmarshal(b []byte) (Signed, error) {
	if len(b) < 4+2+2 || [4]byte{b[0], b[1], b[2], b[3]} != wireMagic {
		return Signed{}, ErrMalformed
	}
	off := 4
	keyIDLen := int(binary.BigEndian.Uint16(b[off : off+2]))
	off += 2
	if off+keyIDLen+2 > len(b) {
		return Signed{}, ErrMalformed
	}
	keyID := string(b[off : off+keyIDLen])
	off += keyIDLen

	sigLen := int(binary.BigEndian.Uint16(b[off : off+2]))
	off += 2
	if off+sigLen > len(b) {
		return Signed{}, ErrMalformed
	}
	sig := make([]byte, sigLen)
	copy(sig, b[off:off+sigLen])
	off += sigLen

	a, err := Decode(b[off:])
	if err != nil {
		return Signed{}, err
	}
	return Signed{Artifact: a, KeyID: keyID, Signature: sig}, nil
}

// Open parses a wire blob AND verifies its signature against v. It returns the
// recovered artifact only if the signature is valid. This is the function every
// channel consumer calls: bytes in, trusted artifact out (or an error).
func Open(b []byte, v Verifier) (Artifact, error) {
	s, err := Unmarshal(b)
	if err != nil {
		return Artifact{}, err
	}
	canon, err := s.Artifact.Canonical()
	if err != nil {
		return Artifact{}, err
	}
	if !v.Verify(s.KeyID, canon, s.Signature) {
		return Artifact{}, fmt.Errorf("%w (key %q)", ErrBadSignature, s.KeyID)
	}
	return s.Artifact, nil
}

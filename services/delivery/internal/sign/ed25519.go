// Package sign provides Ed25519 signing and a rotating multi-key verifier for
// delivery artifacts. Every artifact put on any channel is signed here; every
// consumer verifies here. Ed25519 gives small (64-byte) signatures — important
// for the DNS TXT and steganographic channels, which are byte-budget constrained.
package sign

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
)

// Signer holds one private key and its KeyID. The KeyID travels with every
// signature so a verifier can select the matching public key across rotations.
type Signer struct {
	keyID string
	priv  ed25519.PrivateKey
}

// NewSigner builds a Signer from a KeyID and a raw Ed25519 private key.
func NewSigner(keyID string, priv ed25519.PrivateKey) (*Signer, error) {
	if keyID == "" {
		return nil, errors.New("sign: keyID required")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("sign: private key must be %d bytes, got %d", ed25519.PrivateKeySize, len(priv))
	}
	return &Signer{keyID: keyID, priv: priv}, nil
}

// SignerFromSeed builds a Signer from a 32-byte seed (the form stored in config,
// base64). The seed deterministically derives the key pair, so the same seed
// always yields the same public key — verifiers stay valid across restarts.
func SignerFromSeed(keyID string, seed []byte) (*Signer, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("sign: seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	return NewSigner(keyID, ed25519.NewKeyFromSeed(seed))
}

// KeyID reports the signer's key identifier.
func (s *Signer) KeyID() string { return s.keyID }

// Sign returns a detached Ed25519 signature over msg.
func (s *Signer) Sign(msg []byte) []byte { return ed25519.Sign(s.priv, msg) }

// PublicKey returns the signer's public key, for registering in a Verifier.
func (s *Signer) PublicKey() ed25519.PublicKey {
	return s.priv.Public().(ed25519.PublicKey)
}

// Verifier is a keyring: KeyID -> public key. Holding several keys lets a client
// accept artifacts signed by the current OR a recent signing key, so key rotation
// never causes a delivery outage (the anti-block "no single point of failure"
// rule applied to signing).
type Verifier struct {
	keys map[string]ed25519.PublicKey
}

// NewVerifier builds an empty keyring.
func NewVerifier() *Verifier {
	return &Verifier{keys: make(map[string]ed25519.PublicKey)}
}

// Add registers a public key under a KeyID. Re-adding the same KeyID replaces it.
func (v *Verifier) Add(keyID string, pub ed25519.PublicKey) error {
	if keyID == "" {
		return errors.New("sign: keyID required")
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("sign: public key must be %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	v.keys[keyID] = pub
	return nil
}

// Verify reports whether sig is a valid signature over msg by the key named keyID.
// An unknown KeyID returns false — never a soft pass.
func (v *Verifier) Verify(keyID string, msg, sig []byte) bool {
	pub, ok := v.keys[keyID]
	if !ok {
		return false
	}
	return ed25519.Verify(pub, msg, sig)
}

// KeyIDs returns the registered key ids (unordered), for diagnostics.
func (v *Verifier) KeyIDs() []string {
	out := make([]string, 0, len(v.keys))
	for k := range v.keys {
		out = append(out, k)
	}
	return out
}

// DecodeSeed decodes a standard-base64 32-byte Ed25519 seed from config.
func DecodeSeed(b64 string) ([]byte, error) {
	seed, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("sign: bad base64 seed: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("sign: seed must decode to %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	return seed, nil
}

// DecodePublicKey decodes a standard-base64 Ed25519 public key from config.
func DecodePublicKey(b64 string) (ed25519.PublicKey, error) {
	pub, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("sign: bad base64 public key: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("sign: public key must decode to %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	return ed25519.PublicKey(pub), nil
}

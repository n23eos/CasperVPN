// Package secret generates identifiers and per-user isolation material, and
// hashes subscription tokens. All randomness comes from crypto/rand.
package secret

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// UUIDv4 returns a random RFC-4122 v4 UUID string.
func UUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("secret: uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// RealityShortID returns a REALITY short-id: 16 lowercase hex chars (8 bytes).
// Each user gets their own — this is the core of per-user isolation. 8 bytes is
// the maximum REALITY allows; using the full width keeps birthday collisions
// negligible across the whole user base (4 bytes collides around ~65k users,
// which would silently merge two users' isolation domains).
func RealityShortID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("secret: short-id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// PrivateKey returns 32 bytes of standard-base64 key material for a user's own
// transport secrets (server-side only, never rendered into subscriptions).
func PrivateKey() (string, error) {
	return randBase64(32, base64.StdEncoding)
}

// Token returns a high-entropy opaque subscription token (32 bytes, url-safe).
func Token() (string, error) {
	return randBase64(32, base64.RawURLEncoding)
}

// NodeID returns a generated node id when the caller supplies none.
func NodeID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("secret: node id: %w", err)
	}
	return "node-" + hex.EncodeToString(b[:]), nil
}

// HashToken returns the hex SHA-256 of a subscription token. Tokens are high
// entropy, so a fast hash is sufficient — we store only this, never plaintext.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Prefix returns the first n chars of a token for non-secret display/debug.
func Prefix(token string, n int) string {
	if len(token) < n {
		return token
	}
	return token[:n]
}

func randBase64(n int, enc *base64.Encoding) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("secret: rand: %w", err)
	}
	return enc.EncodeToString(b), nil
}

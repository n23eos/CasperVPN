// Package seal encrypts the DIRECTORY pointer before it is placed on a public
// channel (DNS TXT, DoH, git-raw, steganographic carrier). The anti-block rule is
// strict: public channels carry only an ENCRYPTED pointer to where the real
// config lives — never a user secret in the clear. A passive observer of a TXT
// record or a raw file sees ciphertext, not an entry-point address.
//
// AES-256-GCM (stdlib crypto/aes + crypto/cipher) gives authenticated encryption:
// tampering is detected on Open. The key is supplied from config (env/secret
// manager), NEVER hardcoded (CLAUDE.md [ANTI-BLOCK] rule 5, security.md).
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// Sealer encrypts and decrypts pointer blobs with a symmetric AES-256-GCM key.
// It is the shared secret baked into clients (they ship with the key so they can
// read directories); it protects against network observers, not against a client
// that already has the app. Rotate by shipping a new key alongside the old.
type Sealer struct {
	aead cipher.AEAD
}

// New builds a Sealer from a 32-byte key.
func New(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("seal: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("seal: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("seal: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext and returns nonce||ciphertext. A fresh random nonce is
// prepended each call, so identical directories produce different ciphertext (no
// pattern for a censor to fingerprint across channels or over time).
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("seal: nonce: %w", err)
	}
	// Seal appends ciphertext to nonce, so the result is nonce||ciphertext||tag.
	return s.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// ErrCiphertextTooShort is returned when a blob is shorter than a nonce.
var ErrCiphertextTooShort = errors.New("seal: ciphertext shorter than nonce")

// Open reverses Seal: it splits the nonce, then decrypts and authenticates. A
// tampered or wrong-key blob returns an error (GCM auth failure), never garbage.
func (s *Sealer) Open(blob []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(blob) < ns {
		return nil, ErrCiphertextTooShort
	}
	nonce, ct := blob[:ns], blob[ns:]
	pt, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("seal: open: %w", err)
	}
	return pt, nil
}

// DecodeKey decodes a standard-base64 32-byte key from config.
func DecodeKey(b64 string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("seal: bad base64 key: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("seal: key must decode to %d bytes, got %d", KeySize, len(key))
	}
	return key, nil
}

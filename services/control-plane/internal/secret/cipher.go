package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
)

// TokenCipher encrypts recoverable delivery tokens; plaintext never reaches SQL.
type TokenCipher struct {
	aead cipher.AEAD
	ID   string
}

func NewTokenCipher(key []byte) (*TokenCipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("subscription token key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(key)
	return &TokenCipher{aead: aead, ID: hex.EncodeToString(digest[:8])}, nil
}
func (c *TokenCipher) Seal(subID, token string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, []byte(token), []byte(subID)), nil
}
func (c *TokenCipher) Open(subID, keyID string, data []byte) (string, error) {
	if keyID != c.ID || len(data) < c.aead.NonceSize() {
		return "", fmt.Errorf("delivery token key unavailable or ciphertext invalid")
	}
	n := c.aead.NonceSize()
	b, err := c.aead.Open(nil, data[:n], data[n:], []byte(subID))
	if err != nil {
		return "", fmt.Errorf("delivery token authentication failed")
	}
	return string(b), nil
}

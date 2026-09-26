package secret

import (
	"bytes"
	"testing"
)

func TestTokenCipherBindingAndTamper(t *testing.T) {
	c, err := NewTokenCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := c.Seal("sub-a", "private-token")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("private-token")) {
		t.Fatal("plaintext persisted")
	}
	plain, err := c.Open("sub-a", c.ID, enc)
	if err != nil || plain != "private-token" {
		t.Fatal(err)
	}
	if _, err = c.Open("sub-b", c.ID, enc); err == nil {
		t.Fatal("ciphertext can be moved between accounts")
	}
	if _, err = c.Open("sub-a", "other-key", enc); err == nil {
		t.Fatal("unknown key accepted")
	}
	enc[len(enc)-1] ^= 1
	if _, err = c.Open("sub-a", c.ID, enc); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

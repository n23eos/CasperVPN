package seal

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("key: %v", err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := New(newKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pt := []byte("encrypted pointer to entry point")
	blob, err := s.Seal(pt)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := s.Open(blob)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("plaintext mismatch: got %q want %q", got, pt)
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	// Fresh nonce each call => identical plaintext must not yield identical blobs.
	s, _ := New(newKey(t))
	pt := []byte("same pointer")
	a, _ := s.Seal(pt)
	b, _ := s.Seal(pt)
	if bytes.Equal(a, b) {
		t.Fatalf("ciphertext must differ across seals (nonce reuse)")
	}
}

func TestOpenRejectsTamper(t *testing.T) {
	s, _ := New(newKey(t))
	blob, _ := s.Seal([]byte("pointer"))
	blob[len(blob)-1] ^= 0xFF // corrupt the GCM tag
	if _, err := s.Open(blob); err == nil {
		t.Fatalf("expected auth failure on tampered ciphertext")
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	s1, _ := New(newKey(t))
	s2, _ := New(newKey(t))
	blob, _ := s1.Seal([]byte("pointer"))
	if _, err := s2.Open(blob); err == nil {
		t.Fatalf("expected failure decrypting with wrong key")
	}
}

func TestOpenRejectsShortBlob(t *testing.T) {
	s, _ := New(newKey(t))
	if _, err := s.Open([]byte{1, 2, 3}); err == nil {
		t.Fatalf("expected error for too-short blob")
	}
}

func TestNewRejectsWrongKeySize(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatalf("expected error for wrong key size")
	}
}

func TestDecodeKey(t *testing.T) {
	k := newKey(t)
	got, err := DecodeKey(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatalf("DecodeKey: %v", err)
	}
	if !bytes.Equal(got, k) {
		t.Fatalf("key round-trip mismatch")
	}
}

package sign

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func newSeed(t *testing.T) []byte {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return seed
}

func TestSignVerifyRoundTrip(t *testing.T) {
	// Arrange
	signer, err := SignerFromSeed("k1", newSeed(t))
	if err != nil {
		t.Fatalf("SignerFromSeed: %v", err)
	}
	v := NewVerifier()
	if err := v.Add(signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	msg := []byte("canonical artifact bytes")

	// Act
	sig := signer.Sign(msg)

	// Assert
	if !v.Verify("k1", msg, sig) {
		t.Fatalf("valid signature rejected")
	}
	if v.Verify("k1", []byte("tampered"), sig) {
		t.Fatalf("tampered message accepted")
	}
}

func TestVerifyUnknownKeyIsRejected(t *testing.T) {
	signer, _ := SignerFromSeed("k1", newSeed(t))
	v := NewVerifier()
	_ = v.Add("k1", signer.PublicKey())
	if v.Verify("k2", []byte("m"), signer.Sign([]byte("m"))) {
		t.Fatalf("unknown key id must not verify")
	}
}

func TestKeyRotationBothKeysVerify(t *testing.T) {
	// A rotating verifier holds old and new keys so neither breaks clients.
	old, _ := SignerFromSeed("k-old", newSeed(t))
	cur, _ := SignerFromSeed("k-new", newSeed(t))
	v := NewVerifier()
	_ = v.Add("k-old", old.PublicKey())
	_ = v.Add("k-new", cur.PublicKey())

	msg := []byte("m")
	if !v.Verify("k-old", msg, old.Sign(msg)) {
		t.Fatalf("old key should still verify during rotation")
	}
	if !v.Verify("k-new", msg, cur.Sign(msg)) {
		t.Fatalf("new key should verify")
	}
}

func TestDecodeSeedRoundTrip(t *testing.T) {
	seed := newSeed(t)
	b64 := base64.StdEncoding.EncodeToString(seed)
	got, err := DecodeSeed(b64)
	if err != nil {
		t.Fatalf("DecodeSeed: %v", err)
	}
	if base64.StdEncoding.EncodeToString(got) != b64 {
		t.Fatalf("seed round-trip mismatch")
	}
	if _, err := DecodeSeed("not-base64!!"); err == nil {
		t.Fatalf("expected error for bad base64")
	}
	if _, err := DecodeSeed(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatalf("expected error for wrong seed length")
	}
}

func TestSignerConstructorValidation(t *testing.T) {
	if _, err := NewSigner("", make([]byte, 64)); err == nil {
		t.Fatalf("expected error for empty keyID")
	}
	if _, err := NewSigner("k", []byte("tooshort")); err == nil {
		t.Fatalf("expected error for wrong private key size")
	}
	if _, err := SignerFromSeed("k", []byte("short")); err == nil {
		t.Fatalf("expected error for wrong seed size")
	}
}

func TestVerifierAddValidation(t *testing.T) {
	v := NewVerifier()
	if err := v.Add("", make([]byte, 32)); err == nil {
		t.Fatalf("expected error for empty keyID")
	}
	if err := v.Add("k", []byte("short")); err == nil {
		t.Fatalf("expected error for wrong public key size")
	}
	s, _ := SignerFromSeed("k1", newSeed(t))
	_ = v.Add("k1", s.PublicKey())
	ids := v.KeyIDs()
	if len(ids) != 1 || ids[0] != "k1" {
		t.Fatalf("KeyIDs = %v", ids)
	}
}

func TestDecodePublicKeyErrors(t *testing.T) {
	if _, err := DecodePublicKey("not-base64!!"); err == nil {
		t.Fatalf("expected error for bad base64")
	}
	if _, err := DecodePublicKey("YWJj"); err == nil { // decodes to 3 bytes
		t.Fatalf("expected error for wrong public key length")
	}
}

func TestDecodePublicKey(t *testing.T) {
	signer, _ := SignerFromSeed("k1", newSeed(t))
	b64 := base64.StdEncoding.EncodeToString(signer.PublicKey())
	pub, err := DecodePublicKey(b64)
	if err != nil {
		t.Fatalf("DecodePublicKey: %v", err)
	}
	if !pub.Equal(signer.PublicKey()) {
		t.Fatalf("public key round-trip mismatch")
	}
}

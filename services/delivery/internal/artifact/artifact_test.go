package artifact

import (
	"bytes"
	"testing"
	"time"
)

func TestCanonicalRoundTrip(t *testing.T) {
	// Arrange
	a := New(KindSubscription, time.Unix(1_700_000_000, 123).UTC(), []byte("payload-bytes"))

	// Act
	canon, err := a.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	got, err := Decode(canon)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	// Assert
	if got.Kind != a.Kind || got.Version != a.Version {
		t.Fatalf("kind/version mismatch: got %+v want %+v", got, a)
	}
	if !got.IssuedAt.Equal(a.IssuedAt) {
		t.Fatalf("issuedAt mismatch: got %v want %v", got.IssuedAt, a.IssuedAt)
	}
	if !bytes.Equal(got.Payload, a.Payload) {
		t.Fatalf("payload mismatch: got %q want %q", got.Payload, a.Payload)
	}
}

func TestCanonicalIsDeterministic(t *testing.T) {
	a := New(KindDirectory, time.Unix(42, 0).UTC(), []byte("x"))
	b1, _ := a.Canonical()
	b2, _ := a.Canonical()
	if !bytes.Equal(b1, b2) {
		t.Fatalf("Canonical not deterministic")
	}
}

func TestCanonicalRejectsUnknownKind(t *testing.T) {
	a := New(Kind("bogus"), time.Now(), nil)
	if _, err := a.Canonical(); err == nil {
		t.Fatalf("expected error for unknown kind")
	}
}

func TestDecodeRejectsTruncated(t *testing.T) {
	a := New(KindDirectory, time.Now(), []byte("hello"))
	canon, _ := a.Canonical()
	for _, n := range []int{0, 3, len(canon) - 1} {
		if _, err := Decode(canon[:n]); err == nil {
			t.Fatalf("expected error decoding truncated len=%d", n)
		}
	}
}

func TestDecodeRejectsTrailingGarbage(t *testing.T) {
	a := New(KindDirectory, time.Now(), []byte("hello"))
	canon, _ := a.Canonical()
	if _, err := Decode(append(canon, 0xFF)); err == nil {
		t.Fatalf("expected error for trailing garbage")
	}
}

// fakeSigner/fakeVerifier let signed_test exercise the envelope without pulling
// in the real sign package (which would be an import cycle for artifact tests).
type fakeSigner struct{ id string }

func (f fakeSigner) KeyID() string        { return f.id }
func (f fakeSigner) Sign(m []byte) []byte { return append([]byte("sig:"), m...) }

type fakeVerifier struct{ id string }

func (f fakeVerifier) Verify(keyID string, msg, sig []byte) bool {
	return keyID == f.id && bytes.Equal(sig, append([]byte("sig:"), msg...))
}

func TestSignOpenRoundTrip(t *testing.T) {
	a := New(KindSubscription, time.Unix(1, 0).UTC(), []byte("body"))
	s, err := Sign(a, fakeSigner{id: "k1"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	wire, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := Open(wire, fakeVerifier{id: "k1"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Kind != a.Kind || !bytes.Equal(got.Payload, a.Payload) {
		t.Fatalf("recovered artifact mismatch")
	}
}

func TestOpenRejectsTamperedPayload(t *testing.T) {
	a := New(KindDirectory, time.Unix(1, 0).UTC(), []byte("body"))
	s, _ := Sign(a, fakeSigner{id: "k1"})
	wire, _ := s.Marshal()
	// Flip the last byte (inside the payload region) — signature must fail.
	wire[len(wire)-1] ^= 0xFF
	if _, err := Open(wire, fakeVerifier{id: "k1"}); err == nil {
		t.Fatalf("expected bad-signature error on tampered payload")
	}
}

func TestOpenRejectsUnknownKey(t *testing.T) {
	a := New(KindDirectory, time.Unix(1, 0).UTC(), []byte("body"))
	s, _ := Sign(a, fakeSigner{id: "k1"})
	wire, _ := s.Marshal()
	if _, err := Open(wire, fakeVerifier{id: "other"}); err == nil {
		t.Fatalf("expected failure for unknown key id")
	}
}

func TestUnmarshalRejectsForeignBytes(t *testing.T) {
	if _, err := Unmarshal([]byte("not a delivery blob at all")); err == nil {
		t.Fatalf("expected malformed error for foreign bytes")
	}
}

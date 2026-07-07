package pointer

import (
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/delivery/internal/seal"
	"github.com/caspervpn/delivery/internal/sign"
)

func testKeys(t *testing.T) (*sign.Signer, *sign.Verifier, *seal.Sealer) {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 7)
	}
	signer, err := sign.SignerFromSeed("k1", seed)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	v := sign.NewVerifier()
	_ = v.Add("k1", signer.PublicKey())

	key := make([]byte, seal.KeySize)
	for i := range key {
		key[i] = byte(i + 3)
	}
	s, err := seal.New(key)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	return signer, v, s
}

func sampleDirectory() Directory {
	return Directory{
		Revision: 7,
		IssuedAt: time.Unix(1_700_000_000, 0).UTC(),
		EntryPoints: []EntryPoint{{
			NodeID:           "node-a",
			Region:           "eu-central",
			Transports:       []contracts.TransportType{contracts.TransportVlessReality, contracts.TransportHysteria2},
			SubscriptionBase: "https://sub.example/s",
		}},
		Mirrors: []Mirror{
			{Kind: contracts.DeliveryGitHubRaw, Endpoint: "https://raw.example/repo", Priority: 1},
			{Kind: contracts.DeliveryDoH, Endpoint: "https://resolver.example/dns-query", Priority: 2},
		},
		BotHandles: []string{"@caspersub_bot"},
	}
}

func TestPackUnpackRoundTrip(t *testing.T) {
	signer, v, sealer := testKeys(t)
	dir := sampleDirectory()

	wire, err := Pack(dir, sealer, signer)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	got, err := Unpack(wire, v, sealer)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if got.Revision != dir.Revision || len(got.EntryPoints) != 1 || got.EntryPoints[0].NodeID != "node-a" {
		t.Fatalf("directory mismatch: %+v", got)
	}
	if len(got.Mirrors) != 2 || got.Mirrors[0].Kind != contracts.DeliveryGitHubRaw {
		t.Fatalf("mirrors mismatch: %+v", got.Mirrors)
	}
}

func TestUnpackRejectsTamperedWire(t *testing.T) {
	signer, v, sealer := testKeys(t)
	wire, _ := Pack(sampleDirectory(), sealer, signer)
	wire[len(wire)-1] ^= 0xFF // corrupt sealed payload / signature coverage
	if _, err := Unpack(wire, v, sealer); err == nil {
		t.Fatalf("expected error unpacking tampered wire")
	}
}

func TestUnpackRejectsWrongVerifier(t *testing.T) {
	signer, _, sealer := testKeys(t)
	wire, _ := Pack(sampleDirectory(), sealer, signer)
	other := sign.NewVerifier() // empty keyring => cannot verify
	if _, err := Unpack(wire, other, sealer); err == nil {
		t.Fatalf("expected signature failure with unknown key")
	}
}

func TestUnpackRejectsWrongSealKey(t *testing.T) {
	signer, v, _ := testKeys(t)
	// Pack with the real sealer...
	_, vv, realSealer := testKeys(t)
	_ = v
	wire, _ := Pack(sampleDirectory(), realSealer, signer)
	// ...open with a DIFFERENT seal key (but the correct verifier).
	badKey := make([]byte, seal.KeySize) // all zeros != real key
	badSealer, _ := seal.New(badKey)
	if _, err := Unpack(wire, vv, badSealer); err == nil {
		t.Fatalf("expected seal-open failure with wrong seal key")
	}
}

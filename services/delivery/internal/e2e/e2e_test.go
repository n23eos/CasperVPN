// Package e2e proves the top-level acceptance criterion: an artifact placed on
// ANY single channel is recovered and passes signature verification, and a sealed
// directory pointer decrypts back to the original. Each channel is exercised in
// isolation (a registry of exactly one channel) so a pass means that channel
// alone is a sufficient delivery path — the "no single point of failure" promise.
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/delivery/internal/artifact"
	"github.com/caspervpn/delivery/internal/channel"
	"github.com/caspervpn/delivery/internal/channel/dns"
	"github.com/caspervpn/delivery/internal/channel/gitraw"
	"github.com/caspervpn/delivery/internal/channel/steg"
	"github.com/caspervpn/delivery/internal/channel/telegram"
	"github.com/caspervpn/delivery/internal/memkv"
	"github.com/caspervpn/delivery/internal/pointer"
	"github.com/caspervpn/delivery/internal/seal"
	"github.com/caspervpn/delivery/internal/sign"
)

// --- fakes for the external media ------------------------------------------

type fakeRaw struct{ files map[string][]byte }

func (f fakeRaw) Get(_ context.Context, _, path string) ([]byte, error) {
	b, ok := f.files[path]
	if !ok {
		return nil, channel.ErrNotFound
	}
	return b, nil
}
func (f fakeRaw) Put(_ context.Context, path string, blob []byte) error {
	f.files[path] = blob
	return nil
}

type fakeDNS struct{ recs map[string][]string }

func (f fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	s, ok := f.recs[name]
	if !ok {
		return nil, channel.ErrNotFound
	}
	return s, nil
}
func (f fakeDNS) SetTXT(_ context.Context, name string, strs []string) error {
	f.recs[name] = strs
	return nil
}

type fakeCarrier struct{ covers map[string][]byte }

func (f fakeCarrier) PutCover(_ context.Context, key string, cover []byte) error {
	f.covers[key] = cover
	return nil
}
func (f fakeCarrier) GetCover(_ context.Context, key string) ([]byte, error) {
	c, ok := f.covers[key]
	if !ok {
		return nil, channel.ErrNotFound
	}
	return c, nil
}

// allChannels builds one functional instance of every channel with a fake backend.
func allChannels(t *testing.T) []channel.Channel {
	t.Helper()
	tg, err := telegram.NewChannel(memkv.New())
	if err != nil {
		t.Fatalf("telegram: %v", err)
	}
	// One shared fake is both writer and reader so a Put is visible to Get.
	shared := fakeRaw{files: map[string][]byte{}}
	gr, err := gitraw.New([]string{"https://m1", "https://m2"}, shared, shared)
	if err != nil {
		t.Fatalf("gitraw: %v", err)
	}

	txtBackend := fakeDNS{recs: map[string][]string{}}
	txt, err := dns.NewTXT("cfg.example.", txtBackend, txtBackend)
	if err != nil {
		t.Fatalf("dns txt: %v", err)
	}
	dohBackend := fakeDNS{recs: map[string][]string{}}
	doh, err := dns.NewDoH("cfg.example.", dohBackend, dohBackend)
	if err != nil {
		t.Fatalf("dns doh: %v", err)
	}
	st, err := steg.New(fakeCarrier{covers: map[string][]byte{}})
	if err != nil {
		t.Fatalf("steg: %v", err)
	}
	return []channel.Channel{tg, gr, txt, doh, st}
}

func keys(t *testing.T) (*sign.Signer, *sign.Verifier, *seal.Sealer) {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i*2 + 1)
	}
	signer, err := sign.SignerFromSeed("k1", seed)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	v := sign.NewVerifier()
	_ = v.Add("k1", signer.PublicKey())
	sk := make([]byte, seal.KeySize)
	for i := range sk {
		sk[i] = byte(i + 11)
	}
	sealer, err := seal.New(sk)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	return signer, v, sealer
}

func sampleDirectory() pointer.Directory {
	return pointer.Directory{
		Revision: 3,
		IssuedAt: time.Unix(1_700_000_000, 0).UTC(),
		EntryPoints: []pointer.EntryPoint{{
			NodeID:           "node-a",
			Region:           "eu-central",
			Transports:       []contracts.TransportType{contracts.TransportVlessReality},
			SubscriptionBase: "https://sub.example/s",
		}},
		Mirrors:    []pointer.Mirror{{Kind: contracts.DeliveryDNSTXT, Endpoint: "cfg.example.", Priority: 1}},
		BotHandles: []string{"@caspersub_bot"},
	}
}

// TestSignedDirectoryRecoversThroughEveryChannel is the acceptance test: the same
// signed+sealed directory blob, published to one channel at a time, is resolved
// back, its signature verifies, and it decrypts to the original directory.
func TestSignedDirectoryRecoversThroughEveryChannel(t *testing.T) {
	signer, verifier, sealer := keys(t)
	dir := sampleDirectory()
	wire, err := pointer.Pack(dir, sealer, signer)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	for _, ch := range allChannels(t) {
		ch := ch
		t.Run(string(ch.Kind()), func(t *testing.T) {
			// Registry of exactly ONE channel — prove it alone suffices.
			reg := channel.NewRegistry()
			reg.Add(ch, 0)

			if err := ch.Publish(context.Background(), "directory", wire); err != nil {
				t.Fatalf("Publish: %v", err)
			}

			// Resolve verifies the signature as part of Resolve.
			res, err := channel.NewResolver(reg, verifier).Resolve(context.Background(), "directory")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.Channel != ch.Kind() {
				t.Fatalf("resolved via %s, expected %s", res.Channel, ch.Kind())
			}
			if res.Artifact.Kind != artifact.KindDirectory {
				t.Fatalf("recovered artifact kind = %s", res.Artifact.Kind)
			}

			// Full path: unpack (verify sig + decrypt seal) back to the directory.
			blob, err := ch.Fetch(context.Background(), "directory")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			got, err := pointer.Unpack(blob, verifier, sealer)
			if err != nil {
				t.Fatalf("Unpack: %v", err)
			}
			if got.Revision != dir.Revision || len(got.EntryPoints) != 1 ||
				got.EntryPoints[0].NodeID != "node-a" {
				t.Fatalf("recovered directory mismatch: %+v", got)
			}
		})
	}
}

// TestTamperedBlobRejectedOnEveryChannel proves each channel's payload is
// signature-protected: a single flipped bit anywhere makes Resolve reject it.
func TestTamperedBlobRejectedOnEveryChannel(t *testing.T) {
	signer, verifier, sealer := keys(t)
	wire, err := pointer.Pack(sampleDirectory(), sealer, signer)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	tampered := make([]byte, len(wire))
	copy(tampered, wire)
	tampered[len(tampered)-1] ^= 0xFF

	for _, ch := range allChannels(t) {
		ch := ch
		t.Run(string(ch.Kind()), func(t *testing.T) {
			reg := channel.NewRegistry()
			reg.Add(ch, 0)
			if err := ch.Publish(context.Background(), "directory", tampered); err != nil {
				t.Fatalf("Publish: %v", err)
			}
			if _, err := channel.NewResolver(reg, verifier).Resolve(context.Background(), "directory"); err == nil {
				t.Fatalf("tampered blob must be rejected on %s", ch.Kind())
			}
		})
	}
}

// TestSubscriptionArtifactRoundTrip proves the per-user subscription path: a
// signed subscription payload (e.g. the base64 URI list) delivered over an
// authenticated channel is recovered and verified.
func TestSubscriptionArtifactRoundTrip(t *testing.T) {
	signer, verifier, _ := keys(t)
	payload := []byte("dmxlc3M6Ly8...base64-sub-list...") // opaque rendered sub
	art := artifact.New(artifact.KindSubscription, time.Unix(1, 0).UTC(), payload)
	sg, err := artifact.Sign(art, signer)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	wire, err := sg.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	ch, _ := telegram.NewChannel(memkv.New())
	_ = ch.Publish(context.Background(), "user-token", wire)
	blob, err := ch.Fetch(context.Background(), "user-token")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := artifact.Open(blob, verifier)
	if err != nil {
		t.Fatalf("Open (verify): %v", err)
	}
	if got.Kind != artifact.KindSubscription || string(got.Payload) != string(payload) {
		t.Fatalf("subscription artifact mismatch")
	}
}

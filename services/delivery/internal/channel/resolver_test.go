package channel

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/delivery/internal/artifact"
	"github.com/caspervpn/delivery/internal/sign"
)

// memChannel is an in-memory channel used to test the registry and resolver. It
// can be told to return raw (possibly corrupt) bytes to simulate a poisoned
// channel that must be skipped by the resolver.
type memChannel struct {
	kind    Kind
	m       map[string][]byte
	failGet bool
}

func newMemChannel(k Kind) *memChannel { return &memChannel{kind: k, m: map[string][]byte{}} }

func (c *memChannel) Kind() Kind { return c.kind }
func (c *memChannel) Publish(_ context.Context, key string, blob []byte) error {
	c.m[key] = blob
	return nil
}
func (c *memChannel) Fetch(_ context.Context, key string) ([]byte, error) {
	if c.failGet {
		return nil, ErrUnavailable
	}
	b, ok := c.m[key]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}
func (c *memChannel) Healthy(context.Context) bool { return !c.failGet }

func newSignerVerifier(t *testing.T) (*sign.Signer, *sign.Verifier) {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	s, err := sign.SignerFromSeed("k1", seed)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	v := sign.NewVerifier()
	_ = v.Add("k1", s.PublicKey())
	return s, v
}

func signedBlob(t *testing.T, s *sign.Signer, payload string) []byte {
	t.Helper()
	art := artifact.New(artifact.KindDirectory, time.Unix(1, 0).UTC(), []byte(payload))
	sg, err := artifact.Sign(art, s)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	b, err := sg.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestRegistryOrdersByPriority(t *testing.T) {
	reg := NewRegistry()
	reg.Add(newMemChannel(KindGitHubRaw), 10)
	reg.Add(newMemChannel(KindTelegram), 1)
	reg.Add(newMemChannel(KindDoH), 5)
	got := reg.Kinds()
	want := []Kind{KindTelegram, KindDoH, KindGitHubRaw}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("priority order = %v, want %v", got, want)
		}
	}
}

func TestRegistryAddReplacesSameKind(t *testing.T) {
	reg := NewRegistry()
	reg.Add(newMemChannel(KindTelegram), 1)
	reg.Add(newMemChannel(KindTelegram), 2)
	if len(reg.Kinds()) != 1 {
		t.Fatalf("re-adding same kind should replace, got %v", reg.Kinds())
	}
}

func TestResolverFailsOverPastPoisonedChannel(t *testing.T) {
	s, v := newSignerVerifier(t)
	reg := NewRegistry()

	// Highest-priority channel returns CORRUPT bytes (poisoned) — must be skipped.
	poison := newMemChannel(KindDoH)
	poison.m["dir"] = []byte("garbage that will not verify")
	reg.Add(poison, 1)

	// Next channel is down entirely.
	down := newMemChannel(KindGitHubRaw)
	down.failGet = true
	reg.Add(down, 2)

	// Lowest-priority channel has the genuine signed blob.
	good := newMemChannel(KindTelegram)
	good.m["dir"] = signedBlob(t, s, "real-directory")
	reg.Add(good, 3)

	res, err := NewResolver(reg, v).Resolve(context.Background(), "dir")
	if err != nil {
		t.Fatalf("Resolve should succeed via good channel: %v", err)
	}
	if res.Channel != KindTelegram {
		t.Fatalf("expected resolution via telegram, got %s", res.Channel)
	}
	if string(res.Artifact.Payload) != "real-directory" {
		t.Fatalf("recovered wrong payload: %q", res.Artifact.Payload)
	}
}

func TestResolverAllFail(t *testing.T) {
	_, v := newSignerVerifier(t)
	reg := NewRegistry()
	down := newMemChannel(KindDoH)
	down.failGet = true
	reg.Add(down, 1)
	if _, err := NewResolver(reg, v).Resolve(context.Background(), "dir"); err == nil {
		t.Fatalf("expected error when all channels fail")
	}
}

func TestRegistryRemoveAndGet(t *testing.T) {
	reg := NewRegistry()
	tg := newMemChannel(KindTelegram)
	reg.Add(tg, 1)
	reg.Add(newMemChannel(KindDoH), 2)

	if got, ok := reg.Get(KindTelegram); !ok || got != tg {
		t.Fatalf("Get should return the telegram channel")
	}
	if _, ok := reg.Get(KindSteg); ok {
		t.Fatalf("Get for unregistered kind should be false")
	}
	reg.Remove(KindTelegram)
	if _, ok := reg.Get(KindTelegram); ok {
		t.Fatalf("Remove should drop the channel")
	}
	reg.Remove(KindSteg) // no-op, must not panic
	if len(reg.Ordered()) != 1 {
		t.Fatalf("expected 1 channel left, got %d", len(reg.Ordered()))
	}
}

func TestRegistryHealth(t *testing.T) {
	reg := NewRegistry()
	ok := newMemChannel(KindTelegram)
	bad := newMemChannel(KindDoH)
	bad.failGet = true
	reg.Add(ok, 1)
	reg.Add(bad, 2)
	h := reg.Health(context.Background())
	if !h[KindTelegram] || h[KindDoH] {
		t.Fatalf("health = %v", h)
	}
}

func TestResolverHonorsCanceledContext(t *testing.T) {
	_, v := newSignerVerifier(t)
	reg := NewRegistry()
	reg.Add(newMemChannel(KindTelegram), 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewResolver(reg, v).Resolve(ctx, "dir"); err == nil {
		t.Fatalf("expected error on canceled context")
	}
}

func TestPublisherBroadcast(t *testing.T) {
	reg := NewRegistry()
	a := newMemChannel(KindTelegram)
	b := newMemChannel(KindDoH)
	reg.Add(a, 1)
	reg.Add(b, 2)
	errs := NewPublisher(reg).Broadcast(context.Background(), "dir", []byte("blob"))
	if errs[KindTelegram] != nil || errs[KindDoH] != nil {
		t.Fatalf("broadcast errors: %v", errs)
	}
	if string(a.m["dir"]) != "blob" || string(b.m["dir"]) != "blob" {
		t.Fatalf("broadcast did not reach all channels")
	}
}

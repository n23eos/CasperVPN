package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/caspervpn/delivery/internal/artifact"
	"github.com/caspervpn/delivery/internal/channel"
	"github.com/caspervpn/delivery/internal/config"
)

func TestBuildEphemeralDefaults(t *testing.T) {
	a, err := Build(config.Config{Port: "8083", SignKeyID: "k"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !a.EphemeralKeys {
		t.Fatalf("expected ephemeral keys with no configured seed/seal")
	}
	// The messenger channel is always registered so there is at least one carrier.
	if _, ok := a.Registry.Get(channel.KindTelegram); !ok {
		t.Fatalf("expected telegram channel registered")
	}
	if a.Handler == nil {
		t.Fatalf("expected an HTTP handler")
	}
	// Signer/verifier must be mutually consistent: what the signer signs here, the
	// app's own verifier must accept.
	art := artifact.New(artifact.KindDirectory, time.Unix(1, 0).UTC(), []byte("x"))
	sg, err := artifact.Sign(art, a.Signer)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	wire, _ := sg.Marshal()
	if _, err := artifact.Open(wire, a.Verifier); err != nil {
		t.Fatalf("app verifier must accept app signer's artifact: %v", err)
	}
}

func TestBuildWithConfiguredKeys(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(seed)
	sealKey := make([]byte, 32)
	_, _ = rand.Read(sealKey)

	cfg := config.Config{
		Port:        "8083",
		SignKeyID:   "prod-1",
		SignSeedB64: base64.StdEncoding.EncodeToString(seed),
		SealKeyB64:  base64.StdEncoding.EncodeToString(sealKey),
	}
	a, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if a.EphemeralKeys {
		t.Fatalf("configured keys must not be flagged ephemeral")
	}
	if a.Signer.KeyID() != "prod-1" {
		t.Fatalf("key id = %q", a.Signer.KeyID())
	}
}

func TestBuildRejectsBadSeed(t *testing.T) {
	if _, err := Build(config.Config{SignSeedB64: "not-base64!!"}); err == nil {
		t.Fatalf("expected error for bad seed")
	}
}

func TestBuildRejectsBadSealKey(t *testing.T) {
	if _, err := Build(config.Config{SealKeyB64: "not-base64!!"}); err == nil {
		t.Fatalf("expected error for bad seal key")
	}
}

func TestBuildRegistersConfiguredChannels(t *testing.T) {
	cfg := config.Config{
		SignKeyID:     "k",
		DNSZone:       "cfg.example.",
		DoHEndpoint:   "https://r.example/dns-query",
		GitRawMirrors: []string{"https://raw.example/repo"},
	}
	a, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []channel.Kind{channel.KindTelegram, channel.KindDoH, channel.KindDNSTXT, channel.KindGitHubRaw} {
		if _, ok := a.Registry.Get(want); !ok {
			t.Fatalf("expected channel %s registered", want)
		}
	}
}

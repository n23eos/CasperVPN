// Package app wires the delivery service from config: it builds the signer,
// verifier and sealer (with dev-only ephemeral fallbacks), constructs every
// configured channel, and assembles the registry, resolver and HTTP handler.
// Keeping the wiring here (not in main) makes the assembled service testable.
package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"

	"github.com/caspervpn/delivery/internal/channel"
	"github.com/caspervpn/delivery/internal/channel/dns"
	"github.com/caspervpn/delivery/internal/channel/gitraw"
	"github.com/caspervpn/delivery/internal/channel/telegram"
	"github.com/caspervpn/delivery/internal/config"
	"github.com/caspervpn/delivery/internal/httpapi"
	"github.com/caspervpn/delivery/internal/memkv"
	"github.com/caspervpn/delivery/internal/seal"
	"github.com/caspervpn/delivery/internal/sign"
)

// App is the assembled delivery service.
type App struct {
	Registry  *channel.Registry
	Resolver  *channel.Resolver
	Publisher *channel.Publisher
	Signer    *sign.Signer
	Verifier  *sign.Verifier
	Sealer    *seal.Sealer
	Handler   http.Handler

	// EphemeralKeys is true when signing/seal keys were generated at boot because
	// none were configured — dev only; a warning is logged by main.
	EphemeralKeys bool
}

// Build assembles an App from config.
func Build(cfg config.Config) (*App, error) {
	signer, verifier, ephemeralSign, err := buildSigning(cfg)
	if err != nil {
		return nil, err
	}
	sealer, ephemeralSeal, err := buildSealer(cfg)
	if err != nil {
		return nil, err
	}

	reg := channel.NewRegistry()
	registerChannels(reg, cfg)

	app := &App{
		Registry:      reg,
		Resolver:      channel.NewResolver(reg, verifier, channel.WithMaxAge(cfg.ArtifactMaxAge)),
		Publisher:     channel.NewPublisher(reg),
		Signer:        signer,
		Verifier:      verifier,
		Sealer:        sealer,
		Handler:       httpapi.New(reg, cfg.AdminToken).Routes(),
		EphemeralKeys: ephemeralSign || ephemeralSeal,
	}
	return app, nil
}

// buildSigning returns a signer + verifier. A configured seed yields a stable key;
// otherwise an ephemeral key is generated (dev only) so the service still boots.
func buildSigning(cfg config.Config) (*sign.Signer, *sign.Verifier, bool, error) {
	verifier := sign.NewVerifier()

	// Additional (rotated/previous) trusted public keys.
	for id, b64 := range cfg.VerifyKeys {
		pub, err := sign.DecodePublicKey(b64)
		if err != nil {
			return nil, nil, false, fmt.Errorf("app: verify key %q: %w", id, err)
		}
		if err := verifier.Add(id, pub); err != nil {
			return nil, nil, false, err
		}
	}

	var signer *sign.Signer
	ephemeral := false
	if cfg.SignSeedB64 != "" {
		seed, err := sign.DecodeSeed(cfg.SignSeedB64)
		if err != nil {
			return nil, nil, false, err
		}
		signer, err = sign.SignerFromSeed(cfg.SignKeyID, seed)
		if err != nil {
			return nil, nil, false, err
		}
	} else {
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, nil, false, fmt.Errorf("app: ephemeral seed: %w", err)
		}
		var err error
		signer, err = sign.SignerFromSeed(cfg.SignKeyID, seed)
		if err != nil {
			return nil, nil, false, err
		}
		ephemeral = true
	}

	// The local signer's own public key is always trusted.
	if err := verifier.Add(signer.KeyID(), signer.PublicKey()); err != nil {
		return nil, nil, false, err
	}
	return signer, verifier, ephemeral, nil
}

// buildSealer returns a sealer from config or an ephemeral key (dev only).
func buildSealer(cfg config.Config) (*seal.Sealer, bool, error) {
	if cfg.SealKeyB64 != "" {
		key, err := seal.DecodeKey(cfg.SealKeyB64)
		if err != nil {
			return nil, false, err
		}
		s, err := seal.New(key)
		return s, false, err
	}
	key := make([]byte, seal.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, false, fmt.Errorf("app: ephemeral seal key: %w", err)
	}
	s, err := seal.New(key)
	return s, true, err
}

// registerChannels adds every CONFIGURED channel. Unconfigured channels are simply
// absent — the delivery set is dynamic and config-driven, never hardcoded.
func registerChannels(reg *channel.Registry, cfg config.Config) {
	priority := 0
	next := func() int { p := priority; priority++; return p }

	// Messenger channel: always available via an in-memory blob store so the
	// service has at least one working carrier in any deployment.
	if ch, err := telegram.NewChannel(memkv.New()); err == nil {
		reg.Add(ch, next())
	}

	// DNS-over-HTTPS (read path over HTTPS).
	if cfg.DNSZone != "" && cfg.DoHEndpoint != "" {
		if ch, err := dns.NewDoH(cfg.DNSZone, dns.DoHClient{Endpoint: cfg.DoHEndpoint}, nil); err == nil {
			reg.Add(ch, next())
		}
	}
	// Plain DNS TXT (read path over Do53).
	if cfg.DNSZone != "" {
		if ch, err := dns.NewTXT(cfg.DNSZone, dns.Do53Resolver{}, nil); err == nil {
			reg.Add(ch, next())
		}
	}
	// Git-raw mirrors.
	if len(cfg.GitRawMirrors) > 0 {
		if ch, err := gitraw.New(cfg.GitRawMirrors, gitraw.HTTPTransport{}, nil); err == nil {
			reg.Add(ch, next())
		}
	}
}

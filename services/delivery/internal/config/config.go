// Package config loads the delivery service configuration from the environment.
// Every channel endpoint, key and zone is config-supplied with a safe default so
// the service boots with zero config in dev. NO domain, IP, mirror or key is
// hardcoded in the binary (CLAUDE.md [ANTI-BLOCK] rule 5, security.md): unset
// channels are simply not registered, keeping the delivery set dynamic.
package config

import (
	"strings"
	"time"

	"github.com/caspervpn/platform/envcfg"
)

const (
	defaultPort = "8083"

	// Bot rate-limit / antispam defaults.
	defaultBotRatePerSec = 1
	defaultBotBurst      = 5
	defaultBotCooldown   = 3 * time.Second
)

// Config is the fully-resolved delivery configuration.
type Config struct {
	Port string

	// AdminToken guards the mutating admin surface (POST /v1/channels). Empty
	// disables that path entirely (fail-closed) — a bare service exposes only
	// read/fetch. Operator-supplied via env; never hardcoded.
	AdminToken string

	// ArtifactMaxAge enables client-side anti-rollback in the resolver: a
	// verified-but-stale artifact older than this is rejected and the resolver
	// fails over to a fresher channel. Zero (default) disables the check.
	ArtifactMaxAge time.Duration

	// Signing. SignSeedB64 is a base64 32-byte Ed25519 seed; empty => generate an
	// ephemeral key at boot (dev only) and log its public key.
	SignKeyID   string
	SignSeedB64 string

	// VerifyKeys are ADDITIONAL trusted public keys (keyID -> base64 pubkey), for
	// accepting artifacts signed by a previous/rotated key. The local signer's own
	// public key is always trusted.
	VerifyKeys map[string]string

	// SealKeyB64 is a base64 32-byte AES-256 key for the pointer seal; empty =>
	// generate an ephemeral key at boot (dev only).
	SealKeyB64 string

	// Channel endpoints — each optional; unset channels are not registered.
	DNSZone       string   // zone for DNS TXT / DoH delivery (e.g. cfg.example.)
	DoHEndpoint   string   // DoH JSON endpoint URL
	GitRawMirrors []string // raw base URLs, tried in order + rotated

	TelegramBase  string
	TelegramToken string
	MaxBase       string
	MaxToken      string

	Bot BotTunables
}

// BotTunables are the messenger rate-limit/antispam knobs.
type BotTunables struct {
	RatePerSec int
	Burst      int
	Cooldown   time.Duration
}

// Load reads configuration from the environment, applying defaults. A
// malformed value (bad int/duration) is an error: startup must fail loudly
// instead of silently running with a default.
func Load() (Config, error) {
	var e envcfg.Env
	cfg := Config{
		Port:           e.Str("PORT", defaultPort),
		AdminToken:     e.Str("DELIVERY_ADMIN_TOKEN", ""),
		ArtifactMaxAge: e.Duration("DELIVERY_ARTIFACT_MAX_AGE", 0),
		SignKeyID:      e.Str("DELIVERY_SIGN_KEY_ID", "delivery-ephemeral"),
		SignSeedB64:    e.Str("DELIVERY_SIGN_SEED", ""),
		VerifyKeys:     parseKeyMap(e.CSV("DELIVERY_VERIFY_KEYS")),
		SealKeyB64:     e.Str("DELIVERY_SEAL_KEY", ""),

		DNSZone:       e.Str("DELIVERY_DNS_ZONE", ""),
		DoHEndpoint:   e.Str("DELIVERY_DOH_ENDPOINT", ""),
		GitRawMirrors: e.CSV("DELIVERY_GITRAW_MIRRORS"),

		TelegramBase:  e.Str("DELIVERY_TELEGRAM_BASE", ""),
		TelegramToken: e.Str("DELIVERY_TELEGRAM_TOKEN", ""),
		MaxBase:       e.Str("DELIVERY_MAX_BASE", ""),
		MaxToken:      e.Str("DELIVERY_MAX_TOKEN", ""),

		Bot: BotTunables{
			RatePerSec: e.Int("DELIVERY_BOT_RATE_PER_SEC", defaultBotRatePerSec),
			Burst:      e.Int("DELIVERY_BOT_BURST", defaultBotBurst),
			Cooldown:   e.Duration("DELIVERY_BOT_COOLDOWN", defaultBotCooldown),
		},
	}
	if err := e.Err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// parseKeyMap parses ["id1:pub1", "id2:pub2"] pairs into a map.
func parseKeyMap(pairs []string) map[string]string {
	out := map[string]string{}
	for _, pair := range pairs {
		if i := strings.IndexByte(pair, ':'); i > 0 {
			out[pair[:i]] = pair[i+1:]
		}
	}
	return out
}

// Package config loads the delivery service configuration from the environment.
// Every channel endpoint, key and zone is config-supplied with a safe default so
// the service boots with zero config in dev. NO domain, IP, mirror or key is
// hardcoded in the binary (CLAUDE.md [ANTI-BLOCK] rule 5, security.md): unset
// channels are simply not registered, keeping the delivery set dynamic.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
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

// Load reads configuration from the environment, applying defaults.
func Load() Config {
	return Config{
		Port:        getStr("PORT", defaultPort),
		SignKeyID:   getStr("DELIVERY_SIGN_KEY_ID", "delivery-ephemeral"),
		SignSeedB64: getStr("DELIVERY_SIGN_SEED", ""),
		VerifyKeys:  parseKeyMap(getStr("DELIVERY_VERIFY_KEYS", "")),
		SealKeyB64:  getStr("DELIVERY_SEAL_KEY", ""),

		DNSZone:       getStr("DELIVERY_DNS_ZONE", ""),
		DoHEndpoint:   getStr("DELIVERY_DOH_ENDPOINT", ""),
		GitRawMirrors: splitCSV(getStr("DELIVERY_GITRAW_MIRRORS", "")),

		TelegramBase:  getStr("DELIVERY_TELEGRAM_BASE", ""),
		TelegramToken: getStr("DELIVERY_TELEGRAM_TOKEN", ""),
		MaxBase:       getStr("DELIVERY_MAX_BASE", ""),
		MaxToken:      getStr("DELIVERY_MAX_TOKEN", ""),

		Bot: BotTunables{
			RatePerSec: getInt("DELIVERY_BOT_RATE_PER_SEC", defaultBotRatePerSec),
			Burst:      getInt("DELIVERY_BOT_BURST", defaultBotBurst),
			Cooldown:   getDur("DELIVERY_BOT_COOLDOWN", defaultBotCooldown),
		},
	}
}

// parseKeyMap parses "id1:pub1,id2:pub2" into a map.
func parseKeyMap(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range splitCSV(s) {
		if i := strings.IndexByte(pair, ':'); i > 0 {
			out[pair[:i]] = pair[i+1:]
		}
	}
	return out
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func getStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

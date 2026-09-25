// Package config loads the delivery service configuration from the environment.
// Every channel endpoint, key and zone is config-supplied with a safe default so
// the service boots with zero config in dev. NO domain, IP, mirror or key is
// hardcoded in the binary (CLAUDE.md [ANTI-BLOCK] rule 5, security.md): unset
// channels are simply not registered, keeping the delivery set dynamic.
package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/platform/envcfg"
)

const (
	defaultPort = "8083"

	// Bot rate-limit / antispam defaults.
	defaultBotRatePerSec = 1
	defaultBotBurst      = 5
	defaultBotCooldown   = 3 * time.Second
	defaultHTTPTimeout   = 10 * time.Second
	defaultPollTimeout   = 25 * time.Second
	defaultRetryDelay    = time.Second
)

// Config is the fully-resolved delivery configuration.
type Config struct {
	Port string
	Env  string

	// BotEnabled is mandatory in production and explicit in dev/test. The
	// opt-in keeps local library and channel work free from external services.
	BotEnabled bool

	DatabaseURL       string
	ControlPlaneBase  string
	ControlPlaneToken string
	BillingBase       string
	BillingToken      string
	PublicSubBase     string
	HTTPTimeout       time.Duration
	PollTimeout       time.Duration
	RetryDelay        time.Duration

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
	DefaultPlan     contracts.SubscriptionPlan
	DefaultCurrency string
}

// Load reads configuration from the environment, applying defaults. A
// malformed value (bad int/duration) is an error: startup must fail loudly
// instead of silently running with a default.
func Load() (Config, error) {
	var e envcfg.Env
	env := e.Str("ENV", "production")
	cfg := Config{
		Port:           e.Str("PORT", defaultPort),
		Env:            env,
		BotEnabled:     e.Bool("DELIVERY_BOT_ENABLED", env == "production"),
		DatabaseURL:       e.Str("DATABASE_URL", ""),
		ControlPlaneBase:  strings.TrimRight(e.Str("DELIVERY_CONTROL_PLANE_BASE", ""), "/"),
		ControlPlaneToken: e.Str("DELIVERY_CONTROL_PLANE_TOKEN", ""),
		BillingBase:       strings.TrimRight(e.Str("DELIVERY_BILLING_BASE", ""), "/"),
		BillingToken:      e.Str("DELIVERY_BILLING_TOKEN", ""),
		PublicSubBase:     strings.TrimRight(e.Str("DELIVERY_PUBLIC_SUBSCRIPTION_BASE", ""), "/"),
		HTTPTimeout:       e.Duration("DELIVERY_HTTP_TIMEOUT", defaultHTTPTimeout),
		PollTimeout:       e.Duration("DELIVERY_TELEGRAM_POLL_TIMEOUT", defaultPollTimeout),
		RetryDelay:        e.Duration("DELIVERY_RETRY_DELAY", defaultRetryDelay),
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
			DefaultPlan: contracts.SubscriptionPlan(e.Str("DELIVERY_BOT_DEFAULT_PLAN", string(contracts.SubscriptionPlanBasic))),
			DefaultCurrency: strings.ToUpper(e.Str("DELIVERY_BOT_DEFAULT_CURRENCY", "XMR")),
		},
	}
	if err := e.Err(); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects partial production and bot configurations before any
// network listener starts. Error messages name variables but never their values.
func (c Config) Validate() error {
	switch c.Env {
	case "production", "dev", "test":
	default:
		return fmt.Errorf("config: ENV must be production, dev, or test")
	}
	if !c.Bot.DefaultPlan.Valid() {
		return fmt.Errorf("config: DELIVERY_BOT_DEFAULT_PLAN is invalid")
	}
	if strings.TrimSpace(c.Bot.DefaultCurrency) == "" {
		return fmt.Errorf("config: DELIVERY_BOT_DEFAULT_CURRENCY is required")
	}
	if c.Env == "production" {
		if !c.BotEnabled {
			return fmt.Errorf("config: DELIVERY_BOT_ENABLED must be true in production")
		}
		if c.SignSeedB64 == "" || c.SealKeyB64 == "" {
			return fmt.Errorf("config: DELIVERY_SIGN_SEED and DELIVERY_SEAL_KEY are required in production")
		}
	}
	if !c.BotEnabled {
		return nil
	}
	required := map[string]string{
		"DATABASE_URL": c.DatabaseURL,
		"DELIVERY_CONTROL_PLANE_BASE": c.ControlPlaneBase,
		"DELIVERY_CONTROL_PLANE_TOKEN": c.ControlPlaneToken,
		"DELIVERY_BILLING_BASE": c.BillingBase,
		"DELIVERY_BILLING_TOKEN": c.BillingToken,
		"DELIVERY_TELEGRAM_BASE": c.TelegramBase,
		"DELIVERY_TELEGRAM_TOKEN": c.TelegramToken,
		"DELIVERY_PUBLIC_SUBSCRIPTION_BASE": c.PublicSubBase,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("config: %s is required when delivery bot is enabled", name)
		}
	}
	if c.HTTPTimeout <= 0 || c.PollTimeout <= 0 || c.RetryDelay <= 0 {
		return fmt.Errorf("config: delivery HTTP, poll, and retry durations must be positive")
	}
	if err := requireURL("DELIVERY_CONTROL_PLANE_BASE", c.ControlPlaneBase, false); err != nil {
		return err
	}
	if err := requireURL("DELIVERY_BILLING_BASE", c.BillingBase, false); err != nil {
		return err
	}
	if err := requireURL("DELIVERY_TELEGRAM_BASE", c.TelegramBase, c.Env == "production"); err != nil {
		return err
	}
	if err := requireURL("DELIVERY_PUBLIC_SUBSCRIPTION_BASE", c.PublicSubBase, true); err != nil {
		return err
	}
	return nil
}

func requireURL(name, raw string, httpsOnly bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("config: %s must be an absolute HTTP URL", name)
	}
	if httpsOnly && u.Scheme != "https" {
		return fmt.Errorf("config: %s must use HTTPS", name)
	}
	return nil
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

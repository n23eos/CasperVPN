// Package config loads runtime configuration from the environment. Secrets
// (service tokens, DB DSN) come from env only — never from source.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/caspervpn/control-plane/internal/authz"
	"github.com/caspervpn/platform/envcfg"
)

// Config is the resolved control-plane configuration.
type Config struct {
	SubscriptionTokenKey []byte
	Port                 string
	DatabaseURL          string
	Env                  string
	Tokens               map[string]authz.Role
	RebuildWorkers       int
	RebuildBuffer        int
	// RebuildDurable selects the durable Postgres-backed rebuild queue instead of
	// the in-memory one (env REBUILD_DURABLE). Durable jobs survive restarts and
	// can be drained by several instances; default false keeps the in-memory path.
	RebuildDurable bool

	// SubscriptionInternalURL is the base URL of the subscription service's
	// /internal/* API (env SUBSCRIPTION_INTERNAL_URL). Empty => revocation
	// propagation is a no-op (dev). See TZ-token-revocation.
	SubscriptionInternalURL string
	// SubscriptionInternalToken guards those calls (env SUBSCRIPTION_INTERNAL_TOKEN;
	// must match the subscription service's INTERNAL_TOKEN).
	SubscriptionInternalToken string
	// SubscriptionTimeoutSeconds bounds each outgoing notify call
	// (env SUBSCRIPTION_TIMEOUT_SECONDS, default 5).
	SubscriptionTimeoutSeconds int
}

// Load reads configuration from the environment and validates it.
func Load() (Config, error) {
	var e envcfg.Env
	c := Config{
		Port:           e.Str("PORT", "8081"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		Env:            e.Str("ENV", "production"),
		RebuildWorkers: e.Int("REBUILD_WORKERS", 4),
		RebuildBuffer:  e.Int("REBUILD_BUFFER", 1024),
		RebuildDurable: e.Bool("REBUILD_DURABLE", false),

		SubscriptionInternalURL:    os.Getenv("SUBSCRIPTION_INTERNAL_URL"),
		SubscriptionInternalToken:  os.Getenv("SUBSCRIPTION_INTERNAL_TOKEN"),
		SubscriptionTimeoutSeconds: e.Int("SUBSCRIPTION_TIMEOUT_SECONDS", 5),
	}
	if err := e.Err(); err != nil {
		return Config{}, err
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("config: DATABASE_URL is required")
	}
	if c.SubscriptionInternalURL != "" && c.SubscriptionInternalToken == "" {
		return Config{}, fmt.Errorf("config: SUBSCRIPTION_INTERNAL_TOKEN is required when SUBSCRIPTION_INTERNAL_URL is set")
	}

	if c.Env != "dev" && c.Env != "test" && c.Env != "production" && c.Env != "prod" {
		return Config{}, fmt.Errorf("config: ENV must be dev, test or production")
	}
	rawKey := os.Getenv("SUBSCRIPTION_TOKEN_KEY")
	if rawKey == "" && (c.Env == "dev" || c.Env == "test") {
		c.SubscriptionTokenKey = make([]byte, 32)
		if _, err := rand.Read(c.SubscriptionTokenKey); err != nil {
			return Config{}, err
		}
	} else {
		key, err := base64.StdEncoding.DecodeString(rawKey)
		if err != nil || len(key) != 32 {
			return Config{}, fmt.Errorf("config: SUBSCRIPTION_TOKEN_KEY must be base64 encoding of 32 bytes")
		}
		c.SubscriptionTokenKey = key
	}
	if c.Env == "prod" || c.Env == "production" {
		c.RebuildDurable = true
		if os.Getenv("SEED") == "true" {
			return Config{}, fmt.Errorf("config: SEED is forbidden in production")
		}
	}
	tokens, err := parseTokens(os.Getenv("CONTROL_PLANE_TOKENS"))
	if err != nil {
		return Config{}, err
	}
	if len(tokens) == 0 {
		if c.Env != "dev" && c.Env != "test" {
			return Config{}, fmt.Errorf("config: CONTROL_PLANE_TOKENS is required outside dev")
		}
		// Dev-only fallback tokens (local compose). NEVER use in prod.
		tokens = map[string]authz.Role{
			"dev-admin-token":        authz.RoleAdmin,
			"dev-orchestrator-token": authz.RoleOrchestrator,
			"dev-telemetry-token":    authz.RoleTelemetry,
			"dev-subscription-token": authz.RoleSubscription,
			"dev-billing-token":      authz.RoleBilling,
			"dev-delivery-token":     authz.RoleDelivery,
		}
	}
	if c.Env == "prod" || c.Env == "production" {
		roles := map[authz.Role]bool{}
		for token, role := range tokens {
			if len(token) < 32 || strings.HasPrefix(token, "dev-") {
				return Config{}, fmt.Errorf("config: production service tokens must be unique random values of at least 32 characters")
			}
			roles[role] = true
		}
		for _, r := range []authz.Role{authz.RoleAdmin, authz.RoleOrchestrator, authz.RoleTelemetry, authz.RoleSubscription, authz.RoleBilling, authz.RoleDelivery} {
			if !roles[r] {
				return Config{}, fmt.Errorf("config: service token for role %s is required", r)
			}
		}
	}
	c.Tokens = tokens
	return c, nil
}

// parseTokens parses "token:role,token:role" into a map.
func parseTokens(raw string) (map[string]authz.Role, error) {
	out := map[string]authz.Role{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out, nil
	}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		idx := strings.LastIndex(pair, ":")
		if idx <= 0 || idx == len(pair)-1 {
			return nil, fmt.Errorf("config: invalid service token entry (want token:role)")
		}
		tok := pair[:idx]
		role, ok := authz.ParseRole(pair[idx+1:])
		if !ok {
			return nil, fmt.Errorf("config: unknown service token role")
		}
		if _, exists := out[tok]; exists {
			return nil, fmt.Errorf("config: duplicate service token")
		}
		out[tok] = role
	}
	return out, nil
}

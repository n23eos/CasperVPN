// Package config loads runtime configuration from the environment. Secrets
// (service tokens, DB DSN) come from env only — never from source.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/caspervpn/control-plane/internal/authz"
	"github.com/caspervpn/platform/envcfg"
)

// Config is the resolved control-plane configuration.
type Config struct {
	Port           string
	DatabaseURL    string
	Env            string
	Tokens         map[string]authz.Role
	RebuildWorkers int
	RebuildBuffer  int
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
		Env:            e.Str("ENV", "dev"),
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

	tokens, err := parseTokens(os.Getenv("CONTROL_PLANE_TOKENS"))
	if err != nil {
		return Config{}, err
	}
	if len(tokens) == 0 {
		if c.Env != "dev" {
			return Config{}, fmt.Errorf("config: CONTROL_PLANE_TOKENS is required outside dev")
		}
		// Dev-only fallback tokens (local compose). NEVER use in prod.
		tokens = map[string]authz.Role{
			"dev-admin-token":        authz.RoleAdmin,
			"dev-orchestrator-token": authz.RoleOrchestrator,
			"dev-telemetry-token":    authz.RoleTelemetry,
			"dev-subscription-token": authz.RoleSubscription,
			"dev-billing-token":      authz.RoleBilling,
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
			return nil, fmt.Errorf("config: bad token spec %q (want token:role)", pair)
		}
		tok := pair[:idx]
		role, ok := authz.ParseRole(pair[idx+1:])
		if !ok {
			return nil, fmt.Errorf("config: unknown role in %q", pair)
		}
		out[tok] = role
	}
	return out, nil
}

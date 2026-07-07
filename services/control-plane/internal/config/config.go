// Package config loads runtime configuration from the environment. Secrets
// (service tokens, DB DSN) come from env only — never from source.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/caspervpn/control-plane/internal/authz"
)

// Config is the resolved control-plane configuration.
type Config struct {
	Port           string
	DatabaseURL    string
	Env            string
	Tokens         map[string]authz.Role
	RebuildWorkers int
	RebuildBuffer  int
}

// Load reads configuration from the environment and validates it.
func Load() (Config, error) {
	c := Config{
		Port:           getenv("PORT", "8081"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		Env:            getenv("ENV", "dev"),
		RebuildWorkers: getenvInt("REBUILD_WORKERS", 4),
		RebuildBuffer:  getenvInt("REBUILD_BUFFER", 1024),
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("config: DATABASE_URL is required")
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

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

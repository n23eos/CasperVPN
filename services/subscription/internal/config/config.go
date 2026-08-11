// Package config loads the subscription service configuration from the
// environment and a routing-policy file. No mimicry domain, node IP, DNS server
// or probe URL is hardcoded here: everything censorship-relevant enters as data
// (env vars or the routing-policy JSON file), per the repo [ANTI-BLOCK] rule.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/caspervpn/platform/envcfg"
)

// Defaults that are NOT censorship-relevant (safe to keep in code).
const (
	defaultPort                  = "8082"
	defaultProfileUpdateHours    = 12
	defaultCacheTTL              = 5 * time.Minute
	defaultControlPlaneTimeout   = 5 * time.Second
	defaultRoutingPolicyFilePath = "config/routing.ru.json"
)

// Config is the fully-resolved service configuration.
type Config struct {
	// Port is the HTTP listen port (env PORT).
	Port string

	// ControlPlaneURL is the base URL of the control-plane API (env
	// CONTROL_PLANE_URL). Empty => the in-memory adapter is used (dev/tests).
	ControlPlaneURL string
	// ControlPlaneToken is the internal service-to-service bearer (env
	// CONTROL_PLANE_TOKEN). Never hardcoded.
	ControlPlaneToken string
	// ControlPlaneTimeout bounds every control-plane call.
	ControlPlaneTimeout time.Duration

	// InternalToken guards the /internal/* endpoints (cache invalidation, token
	// registration). Env INTERNAL_TOKEN. Empty => internal endpoints disabled.
	InternalToken string

	// DatabaseURL, when set, backs the subscription-owned token index with a
	// durable Postgres store (survives restart); empty => in-memory (dev). Env
	// DATABASE_URL. Never hardcoded.
	DatabaseURL string

	// ProfileUpdateHours is advertised to clients via Profile-Update-Interval so
	// they self-refresh and blocked nodes wash out quickly. Env
	// PROFILE_UPDATE_INTERVAL_HOURS.
	ProfileUpdateHours int

	// CacheTTL bounds how long a rendered payload is served before rebuild. Env
	// CACHE_TTL (Go duration).
	CacheTTL time.Duration

	// ProfileTitle / SupportURL / ProfileWebPageURL / Announce are Happ UI hints,
	// all optional and config-supplied (no branding hardcoded).
	ProfileTitle      string
	SupportURL        string
	ProfileWebPageURL string
	Announce          string

	// Routing is the split-tunnel policy rendered into sing-box and Clash.
	Routing RoutingPolicy
}

// Load resolves configuration from the environment, reading the routing-policy
// file referenced by ROUTING_POLICY_FILE (default config/routing.ru.json).
// A malformed env value (bad integer/duration) fails startup instead of
// silently falling back to a default.
func Load() (Config, error) {
	var e envcfg.Env
	c := Config{
		Port:                e.Str("PORT", defaultPort),
		ControlPlaneURL:     e.Str("CONTROL_PLANE_URL", ""),
		ControlPlaneToken:   e.Str("CONTROL_PLANE_TOKEN", ""),
		ControlPlaneTimeout: defaultControlPlaneTimeout,
		InternalToken:       e.Str("INTERNAL_TOKEN", ""),
		DatabaseURL:         e.Str("DATABASE_URL", ""),
		ProfileUpdateHours:  e.Int("PROFILE_UPDATE_INTERVAL_HOURS", defaultProfileUpdateHours),
		CacheTTL:            e.Duration("CACHE_TTL", defaultCacheTTL),
		ProfileTitle:        e.Str("SUBSCRIPTION_PROFILE_TITLE", ""),
		SupportURL:          e.Str("SUBSCRIPTION_SUPPORT_URL", ""),
		ProfileWebPageURL:   e.Str("SUBSCRIPTION_WEB_PAGE_URL", ""),
		Announce:            e.Str("SUBSCRIPTION_ANNOUNCE", ""),
	}
	path := e.Str("ROUTING_POLICY_FILE", defaultRoutingPolicyFilePath)
	if err := e.Err(); err != nil {
		return Config{}, err
	}
	if c.ProfileUpdateHours < 1 {
		return Config{}, fmt.Errorf("config: PROFILE_UPDATE_INTERVAL_HOURS must be >= 1")
	}

	rp, err := LoadRoutingPolicy(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: routing policy: %w", err)
	}
	c.Routing = rp
	return c, nil
}

// LoadRoutingPolicy reads and validates a routing-policy JSON file.
func LoadRoutingPolicy(path string) (RoutingPolicy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RoutingPolicy{}, fmt.Errorf("read %s: %w", path, err)
	}
	var rp RoutingPolicy
	// Additive-compatible: unknown fields are tolerated so the policy schema can
	// grow without breaking older deployments.
	if err := json.Unmarshal(raw, &rp); err != nil {
		return RoutingPolicy{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := rp.Validate(); err != nil {
		return RoutingPolicy{}, err
	}
	return rp, nil
}

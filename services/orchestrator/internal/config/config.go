// Package config loads the orchestrator's runtime configuration from the
// environment. Safety posture: DRY_RUN defaults to TRUE — the orchestrator
// plans and logs but never touches infra or the control-plane until an
// operator explicitly sets DRY_RUN=false.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/caspervpn/platform/envcfg"
)

// Config is the full orchestrator configuration. Zero secrets are ever
// hardcoded; tokens and URLs come from env / secret manager only.
type Config struct {
	// Port for the local HTTP server (/healthz).
	Port string

	// DryRun, when true (the default), makes the reconcile loop compute and log
	// the action plan without executing anything.
	DryRun bool

	// ReconcileInterval is the pause between reconcile cycles.
	ReconcileInterval time.Duration

	// Telemetry seam.
	TelemetryURL   string
	TelemetryToken string

	// Control-plane seam.
	ControlPlaneURL   string
	ControlPlaneToken string

	// ScriptsDir holds node_up.sh / node_rotate.sh / node_down.sh.
	ScriptsDir string
	// ScriptTimeout bounds every lifecycle script invocation.
	ScriptTimeout time.Duration

	// Policy thresholds (anti-poisoning gate).
	//
	// RecommendationMaxAge: recommendations older than this are ignored.
	RecommendationMaxAge time.Duration
	// ProbeMaxAge: an own-probe verdict older than this no longer confirms a
	// corroborated recommendation.
	ProbeMaxAge time.Duration
	// DrainGrace: how long a draining node coexists with its replacement before
	// it is retired.
	DrainGrace time.Duration
	// MaxActionsPerCycle caps rotate/replace side effects per reconcile cycle so
	// poisoned input can never stampede the whole fleet (anti-block: diversity).
	MaxActionsPerCycle int

	// Prober.
	ProbeEnabled bool
	ProbeSource  string
	ProbeTimeout time.Duration

	// Placement defaults for replacement provisioning when the retired node
	// carries no placement of its own (never hardcoded — env only).
	DefaultRegion string
	DefaultCloud  string
}

// Load reads configuration from the environment, applying safe defaults.
func Load() (Config, error) {
	var e envcfg.Env
	cfg := Config{
		Port: e.Str("PORT", "8086"),
		// Safety default: dry-run unless explicitly disabled.
		DryRun:               e.Bool("DRY_RUN", true),
		ReconcileInterval:    e.Duration("RECONCILE_INTERVAL", time.Minute),
		TelemetryURL:         os.Getenv("TELEMETRY_URL"),
		TelemetryToken:       os.Getenv("TELEMETRY_TOKEN"),
		ControlPlaneURL:      os.Getenv("CONTROL_PLANE_URL"),
		ControlPlaneToken:    os.Getenv("CONTROL_PLANE_TOKEN"),
		ScriptsDir:           e.Str("SCRIPTS_DIR", "infra/scripts"),
		ScriptTimeout:        e.Duration("SCRIPT_TIMEOUT", 10*time.Minute),
		RecommendationMaxAge: e.Duration("RECOMMENDATION_MAX_AGE", 15*time.Minute),
		ProbeMaxAge:          e.Duration("PROBE_MAX_AGE", 10*time.Minute),
		DrainGrace:           e.Duration("DRAIN_GRACE", 30*time.Minute),
		MaxActionsPerCycle:   1,
		ProbeEnabled:         e.Bool("PROBE_ENABLED", false),
		ProbeSource:          e.Str("PROBE_SOURCE", "orchestrator-local"),
		ProbeTimeout:         e.Duration("PROBE_TIMEOUT", 10*time.Second),
		DefaultRegion:        os.Getenv("DEFAULT_REGION"),
		DefaultCloud:         os.Getenv("DEFAULT_CLOUD"),
	}
	if err := e.Err(); err != nil {
		return Config{}, err
	}
	// envcfg allows zero durations (other services use 0 as "disabled"); every
	// duration here drives a loop or timeout, so all must be strictly positive.
	for _, d := range []struct {
		env string
		val time.Duration
	}{
		{"RECONCILE_INTERVAL", cfg.ReconcileInterval},
		{"SCRIPT_TIMEOUT", cfg.ScriptTimeout},
		{"RECOMMENDATION_MAX_AGE", cfg.RecommendationMaxAge},
		{"PROBE_MAX_AGE", cfg.ProbeMaxAge},
		{"DRAIN_GRACE", cfg.DrainGrace},
		{"PROBE_TIMEOUT", cfg.ProbeTimeout},
	} {
		if d.val <= 0 {
			return Config{}, fmt.Errorf("config: %s must be a positive duration", d.env)
		}
	}
	if v := os.Getenv("MAX_ACTIONS_PER_CYCLE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("config: MAX_ACTIONS_PER_CYCLE must be a positive integer, got %q", v)
		}
		cfg.MaxActionsPerCycle = n
	}
	return cfg, nil
}

// Validate checks that a non-dry-run configuration is complete enough to act.
func (c Config) Validate() error {
	if c.DryRun {
		return nil // planning needs no credentials to be safe
	}
	if c.TelemetryURL == "" {
		return fmt.Errorf("config: TELEMETRY_URL is required when DRY_RUN=false")
	}
	if c.ControlPlaneURL == "" {
		return fmt.Errorf("config: CONTROL_PLANE_URL is required when DRY_RUN=false")
	}
	return nil
}

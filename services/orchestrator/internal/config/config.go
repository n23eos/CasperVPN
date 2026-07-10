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
	cfg := Config{
		Port:                 getenv("PORT", "8086"),
		ReconcileInterval:    time.Minute,
		TelemetryURL:         os.Getenv("TELEMETRY_URL"),
		TelemetryToken:       os.Getenv("TELEMETRY_TOKEN"),
		ControlPlaneURL:      os.Getenv("CONTROL_PLANE_URL"),
		ControlPlaneToken:    os.Getenv("CONTROL_PLANE_TOKEN"),
		ScriptsDir:           getenv("SCRIPTS_DIR", "infra/scripts"),
		ScriptTimeout:        10 * time.Minute,
		RecommendationMaxAge: 15 * time.Minute,
		ProbeMaxAge:          10 * time.Minute,
		DrainGrace:           30 * time.Minute,
		MaxActionsPerCycle:   1,
		ProbeSource:          getenv("PROBE_SOURCE", "orchestrator-local"),
		ProbeTimeout:         10 * time.Second,
		DefaultRegion:        os.Getenv("DEFAULT_REGION"),
		DefaultCloud:         os.Getenv("DEFAULT_CLOUD"),
	}

	var err error
	// Safety default: dry-run unless explicitly disabled.
	if cfg.DryRun, err = getbool("DRY_RUN", true); err != nil {
		return Config{}, err
	}
	if cfg.ProbeEnabled, err = getbool("PROBE_ENABLED", false); err != nil {
		return Config{}, err
	}
	for _, d := range []struct {
		env string
		dst *time.Duration
	}{
		{"RECONCILE_INTERVAL", &cfg.ReconcileInterval},
		{"SCRIPT_TIMEOUT", &cfg.ScriptTimeout},
		{"RECOMMENDATION_MAX_AGE", &cfg.RecommendationMaxAge},
		{"PROBE_MAX_AGE", &cfg.ProbeMaxAge},
		{"DRAIN_GRACE", &cfg.DrainGrace},
		{"PROBE_TIMEOUT", &cfg.ProbeTimeout},
	} {
		if err := getduration(d.env, d.dst); err != nil {
			return Config{}, err
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

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getbool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config: %s must be a boolean, got %q", key, v)
	}
	return b, nil
}

func getduration(key string, dst *time.Duration) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fmt.Errorf("config: %s must be a positive duration, got %q", key, v)
	}
	*dst = d
	return nil
}

// Package config loads the telemetry service configuration from the environment.
// Every tunable has a safe default so the service boots with zero config in dev.
// No secrets or hardcoded domains/IPs live here (see CLAUDE.md [ANTI-BLOCK] rule 5).
package config

import (
	"time"

	"github.com/caspervpn/platform/envcfg"
)

// Default tunables. Named constants — no magic numbers scattered in logic.
const (
	defaultPort = "8085"

	// defaultWindow is the rolling window aggregates and verdicts are computed over.
	defaultWindow = 15 * time.Minute
	// defaultRetention is how long raw anonymous points are kept before pruning.
	defaultRetention = 72 * time.Hour
	// defaultPruneEvery is how often the retention loop runs.
	defaultPruneEvery = 10 * time.Minute

	// Rate limiting (per coarse source, token bucket) + global ceiling.
	defaultRatePerSec  = 5
	defaultRateBurst   = 50
	defaultGlobalRate  = 2000
	defaultGlobalBurst = 5000

	// Batch limits (mirror openapi telemetry.yaml maxItems).
	defaultMaxBatch = 1000

	// Anti-poisoning verdict thresholds. A transport is declared "dead" in a region
	// only when enough INDEPENDENT sources agree AND live traffic is scarce.
	defaultMinSources        = 5    // distinct coarse sources required for any verdict
	defaultMinBlockedSources = 4    // of those, this many must report blocked
	defaultDeadShareThresh   = 0.70 // blocked-source share to call it dead
	defaultMaxOKShareForDead = 0.25 // if more OK sources than this, not dead
	defaultDegradedThresh    = 0.40 // degraded-source share to flag degradation
	defaultSpikeDelta        = 0.30 // recent-vs-older blocked-share jump = spike
	defaultSpikeMinSources   = 3    // min blocked sources in recent half for a spike
)

// Config is the fully-resolved telemetry configuration.
type Config struct {
	Port          string
	InternalToken string // bearer for /v1/health + query/recommendation endpoints
	DatabaseURL   string // when set, use the durable Postgres store (else in-memory)

	Window     time.Duration
	Retention  time.Duration
	PruneEvery time.Duration

	RatePerSec  int
	RateBurst   int
	GlobalRate  int
	GlobalBurst int
	MaxBatch    int

	Verdict VerdictParams
}

// VerdictParams are the anti-poisoning knobs for the aggregator/detector.
type VerdictParams struct {
	MinSources        int
	MinBlockedSources int
	DeadShareThresh   float64
	MaxOKShareForDead float64
	DegradedThresh    float64
	SpikeDelta        float64
	SpikeMinSources   int
}

// Load reads configuration from the environment, applying defaults. A
// malformed value (bad int/float/duration) is an error: startup must fail
// loudly instead of silently running with a default.
func Load() (Config, error) {
	var e envcfg.Env
	cfg := Config{
		Port:          e.Str("PORT", defaultPort),
		InternalToken: e.Str("TELEMETRY_INTERNAL_TOKEN", ""),
		DatabaseURL:   e.Str("DATABASE_URL", ""),

		Window:     e.Duration("TELEMETRY_WINDOW", defaultWindow),
		Retention:  e.Duration("TELEMETRY_RETENTION", defaultRetention),
		PruneEvery: e.Duration("TELEMETRY_PRUNE_EVERY", defaultPruneEvery),

		RatePerSec:  e.Int("TELEMETRY_RATE_PER_SEC", defaultRatePerSec),
		RateBurst:   e.Int("TELEMETRY_RATE_BURST", defaultRateBurst),
		GlobalRate:  e.Int("TELEMETRY_GLOBAL_RATE", defaultGlobalRate),
		GlobalBurst: e.Int("TELEMETRY_GLOBAL_BURST", defaultGlobalBurst),
		MaxBatch:    e.Int("TELEMETRY_MAX_BATCH", defaultMaxBatch),

		Verdict: VerdictParams{
			MinSources:        e.Int("TELEMETRY_MIN_SOURCES", defaultMinSources),
			MinBlockedSources: e.Int("TELEMETRY_MIN_BLOCKED_SOURCES", defaultMinBlockedSources),
			DeadShareThresh:   e.Float("TELEMETRY_DEAD_SHARE", defaultDeadShareThresh),
			MaxOKShareForDead: e.Float("TELEMETRY_MAX_OK_SHARE", defaultMaxOKShareForDead),
			DegradedThresh:    e.Float("TELEMETRY_DEGRADED_SHARE", defaultDegradedThresh),
			SpikeDelta:        e.Float("TELEMETRY_SPIKE_DELTA", defaultSpikeDelta),
			SpikeMinSources:   e.Int("TELEMETRY_SPIKE_MIN_SOURCES", defaultSpikeMinSources),
		},
	}
	if err := e.Err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

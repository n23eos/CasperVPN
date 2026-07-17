// Package config loads the telemetry service configuration from the environment.
// Every tunable has a safe default so the service boots with zero config in dev.
// No secrets or hardcoded domains/IPs live here (see CLAUDE.md [ANTI-BLOCK] rule 5).
package config

import (
	"os"
	"strconv"
	"time"
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

// Load reads configuration from the environment, applying defaults.
func Load() Config {
	return Config{
		Port:          getStr("PORT", defaultPort),
		InternalToken: getStr("TELEMETRY_INTERNAL_TOKEN", ""),
		DatabaseURL:   getStr("DATABASE_URL", ""),

		Window:     getDur("TELEMETRY_WINDOW", defaultWindow),
		Retention:  getDur("TELEMETRY_RETENTION", defaultRetention),
		PruneEvery: getDur("TELEMETRY_PRUNE_EVERY", defaultPruneEvery),

		RatePerSec:  getInt("TELEMETRY_RATE_PER_SEC", defaultRatePerSec),
		RateBurst:   getInt("TELEMETRY_RATE_BURST", defaultRateBurst),
		GlobalRate:  getInt("TELEMETRY_GLOBAL_RATE", defaultGlobalRate),
		GlobalBurst: getInt("TELEMETRY_GLOBAL_BURST", defaultGlobalBurst),
		MaxBatch:    getInt("TELEMETRY_MAX_BATCH", defaultMaxBatch),

		Verdict: VerdictParams{
			MinSources:        getInt("TELEMETRY_MIN_SOURCES", defaultMinSources),
			MinBlockedSources: getInt("TELEMETRY_MIN_BLOCKED_SOURCES", defaultMinBlockedSources),
			DeadShareThresh:   getFloat("TELEMETRY_DEAD_SHARE", defaultDeadShareThresh),
			MaxOKShareForDead: getFloat("TELEMETRY_MAX_OK_SHARE", defaultMaxOKShareForDead),
			DegradedThresh:    getFloat("TELEMETRY_DEGRADED_SHARE", defaultDegradedThresh),
			SpikeDelta:        getFloat("TELEMETRY_SPIKE_DELTA", defaultSpikeDelta),
			SpikeMinSources:   getInt("TELEMETRY_SPIKE_MIN_SOURCES", defaultSpikeMinSources),
		},
	}
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

func getFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
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

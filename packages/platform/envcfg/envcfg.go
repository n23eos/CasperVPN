// Package envcfg reads configuration from the environment with fail-fast
// semantics: an unset variable yields the default, but a MALFORMED value is an
// error — a typo like RATE_PER_SEC=5O must abort startup, not silently fall
// back to a default (the historical behavior in most services, where bad prod
// config degraded without a single log line).
//
// Usage:
//
//	var e envcfg.Env
//	port := e.Str("PORT", "8080")
//	workers := e.Int("WORKERS", 4)
//	if err := e.Err(); err != nil { return err }
package envcfg

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env accumulates parse errors across lookups so Load functions can report
// every bad variable at once instead of one per restart.
type Env struct {
	errs []error
}

// Err returns all accumulated errors, or nil.
func (e *Env) Err() error {
	return errors.Join(e.errs...)
}

func (e *Env) addf(format string, args ...any) {
	e.errs = append(e.errs, fmt.Errorf(format, args...))
}

// Str returns the variable's value, or def when unset/empty.
func (e *Env) Str(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Int parses an integer variable. Unset => def; malformed => error.
func (e *Env) Int(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.addf("config: %s must be an integer, got %q", key, v)
		return def
	}
	return n
}

// Float parses a float variable. Unset => def; malformed => error.
func (e *Env) Float(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		e.addf("config: %s must be a number, got %q", key, v)
		return def
	}
	return f
}

// Bool parses a boolean variable (strconv.ParseBool syntax). Unset => def;
// malformed => error.
func (e *Env) Bool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.addf("config: %s must be a boolean, got %q", key, v)
		return def
	}
	return b
}

// Duration parses a time.Duration variable. Unset => def; malformed or
// negative => error. Zero is allowed (services use it as "disabled").
func (e *Env) Duration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		e.addf("config: %s must be a non-negative duration, got %q", key, v)
		return def
	}
	return d
}

// CSV splits a comma-separated variable into trimmed non-empty items.
// Unset/blank => nil.
func (e *Env) CSV(key string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsAreSafe(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.DryRun {
		t.Fatal("DRY_RUN must default to true — the orchestrator may not act by default")
	}
	if cfg.Port != "8086" {
		t.Errorf("Port = %q, want 8086", cfg.Port)
	}
	if cfg.MaxActionsPerCycle != 1 {
		t.Errorf("MaxActionsPerCycle = %d, want 1", cfg.MaxActionsPerCycle)
	}
	if cfg.ProbeEnabled {
		t.Error("ProbeEnabled must default to false")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("dry-run config must validate without credentials: %v", err)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("DRY_RUN", "false")
	t.Setenv("PORT", "9999")
	t.Setenv("RECONCILE_INTERVAL", "30s")
	t.Setenv("DRAIN_GRACE", "1h")
	t.Setenv("MAX_ACTIONS_PER_CYCLE", "3")
	t.Setenv("TELEMETRY_URL", "http://telemetry:8085")
	t.Setenv("CONTROL_PLANE_URL", "http://control-plane:8081")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.DryRun {
		t.Error("DryRun = true, want false")
	}
	if cfg.Port != "9999" {
		t.Errorf("Port = %q, want 9999", cfg.Port)
	}
	if cfg.ReconcileInterval != 30*time.Second {
		t.Errorf("ReconcileInterval = %v, want 30s", cfg.ReconcileInterval)
	}
	if cfg.DrainGrace != time.Hour {
		t.Errorf("DrainGrace = %v, want 1h", cfg.DrainGrace)
	}
	if cfg.MaxActionsPerCycle != 3 {
		t.Errorf("MaxActionsPerCycle = %d, want 3", cfg.MaxActionsPerCycle)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error: %v", err)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := []struct{ key, val string }{
		{"DRY_RUN", "sometimes"},
		{"PROBE_ENABLED", "yep"},
		{"RECONCILE_INTERVAL", "fast"},
		{"RECONCILE_INTERVAL", "-1m"},
		{"MAX_ACTIONS_PER_CYCLE", "0"},
		{"MAX_ACTIONS_PER_CYCLE", "many"},
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.val, func(t *testing.T) {
			t.Setenv(c.key, c.val)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted %s=%q, want error", c.key, c.val)
			}
		})
	}
}

func TestValidateRequiresSeamsWhenActing(t *testing.T) {
	t.Setenv("DRY_RUN", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate() must fail for DRY_RUN=false without TELEMETRY_URL/CONTROL_PLANE_URL")
	}
	if !strings.Contains(err.Error(), "TELEMETRY_URL") {
		t.Errorf("error should name the missing variable, got: %v", err)
	}
}

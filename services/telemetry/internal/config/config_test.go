package config

import "testing"

func TestProductionRequiresDurableAuthenticatedStorage(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("TELEMETRY_INTERNAL_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("unsafe production boot accepted")
	}
}

func TestExplicitDevAndInvalidTicker(t *testing.T) {
	t.Setenv("ENV", "dev")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEMETRY_PRUNE_EVERY", "0s")
	if _, err := Load(); err == nil {
		t.Fatal("zero ticker would panic")
	}
}

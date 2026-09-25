package config

import (
	"strings"
	"testing"
)

func TestProductionRequiresDurableConfiguration(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("missing database must fail before reading files: %v", err)
	}
}

func TestExplicitDevCanUseFixtureMode(t *testing.T) {
	t.Setenv("ENV", "dev")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("ROUTING_POLICY_FILE", "../../config/routing.ru.json")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicBaseCannotCarryCredentials(t *testing.T) {
	t.Setenv("ENV", "dev")
	t.Setenv("SUBSCRIPTION_PUBLIC_BASE_URL", "https://user:password@example.test")
	if _, err := Load(); err == nil {
		t.Fatal("credential-bearing URL accepted")
	}
}

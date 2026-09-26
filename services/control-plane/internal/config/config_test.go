package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestProductionFailsClosed(t *testing.T) {
	t.Setenv("ENV", "")
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("CONTROL_PLANE_TOKENS", "")
	t.Setenv("SUBSCRIPTION_TOKEN_KEY", "")
	if _, err := Load(); err == nil {
		t.Fatal("implicit environment bypassed production secrets")
	}
	t.Setenv("ENV", "prod")
	t.Setenv("SUBSCRIPTION_TOKEN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if _, err := Load(); err == nil {
		t.Fatal("production lacks service tokens")
	}
	var pairs []string
	for _, role := range []string{"admin", "orchestrator", "telemetry", "subscription", "billing", "delivery"} {
		pairs = append(pairs, strings.Repeat("x", 32)+role+":"+role)
	}
	t.Setenv("CONTROL_PLANE_TOKENS", strings.Join(pairs, ","))
	t.Setenv("SEED", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.RebuildDurable {
		t.Fatal("production queue is volatile")
	}
	t.Setenv("SEED", "true")
	if _, err = Load(); err == nil {
		t.Fatal("production seed accepted")
	}
}
func TestConfigDoesNotEchoSecret(t *testing.T) {
	for _, raw := range []string{"super-private-token", "super-private-token:invalid"} {
		_, err := parseTokens(raw)
		if err == nil || strings.Contains(err.Error(), "super-private-token") {
			t.Fatal("secret echoed in config error", err)
		}
	}
}
func TestExplicitDev(t *testing.T) {
	t.Setenv("ENV", "dev")
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("SUBSCRIPTION_TOKEN_KEY", "")
	t.Setenv("CONTROL_PLANE_TOKENS", "")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

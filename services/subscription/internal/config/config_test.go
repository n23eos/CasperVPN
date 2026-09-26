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

func TestTrustedProxyCIDRsAreExplicitAndValidated(t *testing.T) {
	t.Setenv("ENV", "dev")
	t.Setenv("ROUTING_POLICY_FILE", "../../config/routing.ru.json")
	t.Setenv("SUBSCRIPTION_PUBLIC_BASE_URL", "")
	for _, tc := range []struct {
		raw     string
		count   int
		invalid bool
	}{
		{"", 0, false}, {"172.30.0.0/24, fd00::/64", 2, false}, {"::ffff:172.30.0.0/120", 1, false},
		{"172.30.0.2", 0, true}, {"172.30.0.0/33", 0, true}, {"172.30.0.0/24,", 0, true}, {"*,fd00::/64", 0, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("SUBSCRIPTION_TRUSTED_PROXY_CIDRS", tc.raw)
			cfg, err := Load()
			if (err != nil) != tc.invalid {
				t.Fatalf("err=%v invalid=%t", err, tc.invalid)
			}
			if err == nil && len(cfg.TrustedProxyCIDRs) != tc.count {
				t.Fatal("wrong trusted proxy count")
			}
		})
	}
}

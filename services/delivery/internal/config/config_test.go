package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// With no env set, defaults apply and no channel endpoints are present
	// (unset channels are simply not configured — dynamic, not hardcoded).
	t.Setenv("PORT", "")
	t.Setenv("ENV", "dev")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != defaultPort {
		t.Fatalf("port = %q", cfg.Port)
	}
	if cfg.DNSZone != "" || len(cfg.GitRawMirrors) != 0 {
		t.Fatalf("expected no channel endpoints by default")
	}
	if cfg.Bot.RatePerSec != defaultBotRatePerSec || cfg.Bot.Cooldown != defaultBotCooldown {
		t.Fatalf("bot defaults not applied: %+v", cfg.Bot)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("ENV", "dev")
	t.Setenv("PORT", "9999")
	t.Setenv("DELIVERY_GITRAW_MIRRORS", "https://a/repo, https://b/repo ,")
	t.Setenv("DELIVERY_VERIFY_KEYS", "old:AAAA,new:BBBB")
	t.Setenv("DELIVERY_BOT_COOLDOWN", "10s")
	t.Setenv("DELIVERY_BOT_RATE_PER_SEC", "4")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "9999" {
		t.Fatalf("port = %q", cfg.Port)
	}
	if len(cfg.GitRawMirrors) != 2 || cfg.GitRawMirrors[0] != "https://a/repo" || cfg.GitRawMirrors[1] != "https://b/repo" {
		t.Fatalf("mirrors = %#v", cfg.GitRawMirrors)
	}
	if cfg.VerifyKeys["old"] != "AAAA" || cfg.VerifyKeys["new"] != "BBBB" {
		t.Fatalf("verify keys = %#v", cfg.VerifyKeys)
	}
	if cfg.Bot.Cooldown != 10*time.Second || cfg.Bot.RatePerSec != 4 {
		t.Fatalf("bot tunables = %+v", cfg.Bot)
	}
}

func TestLoadFailsOnMalformedValues(t *testing.T) {
	t.Setenv("ENV", "dev")
	t.Setenv("DELIVERY_BOT_RATE_PER_SEC", "not-an-int")
	t.Setenv("DELIVERY_BOT_COOLDOWN", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("malformed env values must fail Load, got nil error")
	}
}

func TestProductionFailsClosed(t *testing.T) {
	t.Setenv("ENV", "production")
	if _, err := Load(); err == nil {
		t.Fatal("production without stable secrets and bot dependencies must fail")
	}
}

func TestEnabledBotRequiresHTTPSPublicBase(t *testing.T) {
	t.Setenv("ENV", "test")
	t.Setenv("DELIVERY_BOT_ENABLED", "true")
	t.Setenv("DATABASE_URL", "postgres://local/test")
	t.Setenv("DELIVERY_CONTROL_PLANE_BASE", "http://control-plane")
	t.Setenv("DELIVERY_CONTROL_PLANE_TOKEN", "cp-token")
	t.Setenv("DELIVERY_BILLING_BASE", "http://billing")
	t.Setenv("DELIVERY_BILLING_TOKEN", "billing-token")
	t.Setenv("DELIVERY_TELEGRAM_BASE", "http://telegram")
	t.Setenv("DELIVERY_TELEGRAM_TOKEN", "bot-token")
	t.Setenv("DELIVERY_PUBLIC_SUBSCRIPTION_BASE", "http://subscriptions.example")
	if _, err := Load(); err == nil {
		t.Fatal("public subscription base without HTTPS must fail")
	}
}

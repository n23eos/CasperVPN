package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// With no env set, defaults apply and no channel endpoints are present
	// (unset channels are simply not configured — dynamic, not hardcoded).
	t.Setenv("PORT", "")
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
	t.Setenv("DELIVERY_BOT_RATE_PER_SEC", "not-an-int")
	t.Setenv("DELIVERY_BOT_COOLDOWN", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("malformed env values must fail Load, got nil error")
	}
}

package ingest

import (
	"testing"
	"time"
)

func TestRateLimiter_BurstThenBlock(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	clock := now
	rl := NewRateLimiter(1, 3, 1000, 1000, func() time.Time { return clock })
	tok := rl.SourceToken("1.2.3.4:1234")

	// Burst of 3 allowed.
	for i := 0; i < 3; i++ {
		if !rl.Allow(tok, 1) {
			t.Fatalf("burst request %d should be allowed", i)
		}
	}
	// 4th exhausts the per-source bucket.
	if rl.Allow(tok, 1) {
		t.Fatal("4th request should be rate limited")
	}
	// After 2s, 2 tokens refill (rate=1/s).
	clock = clock.Add(2 * time.Second)
	if !rl.Allow(tok, 2) {
		t.Fatal("should be allowed after refill")
	}
}

func TestRateLimiter_GlobalCeiling(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(100, 100, 1, 2, func() time.Time { return now })
	// Global burst is 2; distinct sources still hit the shared ceiling.
	if !rl.Allow(rl.SourceToken("a"), 1) {
		t.Fatal("first allowed")
	}
	if !rl.Allow(rl.SourceToken("b"), 1) {
		t.Fatal("second allowed")
	}
	if rl.Allow(rl.SourceToken("c"), 1) {
		t.Fatal("third should hit global ceiling")
	}
}

func TestRateLimiter_TokenIsSaltedAndStable(t *testing.T) {
	rl := NewRateLimiter(1, 1, 1, 1, nil)
	a := rl.SourceToken("1.2.3.4")
	b := rl.SourceToken("1.2.3.4")
	if a != b {
		t.Fatal("same origin must map to same token within a process")
	}
	if a == "1.2.3.4" {
		t.Fatal("token must not be the raw origin")
	}
}

package httpapi

import (
	"testing"
	"time"
)

// C6: the token bucket allows a burst up to capacity, denies once drained, and
// recovers as tokens refill over time.
func TestTokenBucket_BurstThenDenyThenRefill(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := newTokenBucket(3, 1, func() time.Time { return now }) // cap 3, 1 token/sec

	for i := 0; i < 3; i++ {
		if !b.allow() {
			t.Fatalf("burst token %d denied, want allowed", i)
		}
	}
	if b.allow() {
		t.Fatal("request past capacity allowed, want denied")
	}

	// One second later exactly one token has refilled.
	now = now.Add(time.Second)
	if !b.allow() {
		t.Fatal("after 1s refill: denied, want allowed")
	}
	if b.allow() {
		t.Fatal("second request after single refill allowed, want denied")
	}
}

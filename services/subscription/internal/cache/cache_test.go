package cache

import (
	"fmt"
	"testing"
	"time"
)

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestInvalidateTokenDropsPersonalizedVariants(t *testing.T) {
	c := New(time.Minute, fixedNow(time.Unix(0, 0)))
	e := Entry{Body: []byte("x")}
	c.Put("tok", "singbox", e)
	c.Put("tok", "singbox:RU:ios", e)
	c.Put("tok", "base64:DE:android", e)
	c.Put("other", "singbox:RU:ios", e)

	c.InvalidateToken("tok")

	for _, format := range []string{"singbox", "singbox:RU:ios", "base64:DE:android"} {
		if _, ok := c.Get("tok", format); ok {
			t.Errorf("entry %q survived InvalidateToken", format)
		}
	}
	if _, ok := c.Get("other", "singbox:RU:ios"); !ok {
		t.Error("unrelated token was invalidated")
	}
}

func TestInvalidateTokenDoesNotMatchTokenPrefix(t *testing.T) {
	c := New(time.Minute, fixedNow(time.Unix(0, 0)))
	c.Put("tok2", "singbox", Entry{Body: []byte("x")})

	c.InvalidateToken("tok")

	if _, ok := c.Get("tok2", "singbox"); !ok {
		t.Error("token sharing a prefix was invalidated")
	}
}

func TestSweepBoundsExpiredEntries(t *testing.T) {
	now := time.Unix(0, 0)
	c := New(time.Minute, func() time.Time { return now })

	// Fill past the sweep floor, expire everything, then keep putting fresh
	// entries: the map must shrink instead of retaining every dead entry.
	for i := 0; i < 2000; i++ {
		c.Put("tok", fmt.Sprintf("f%d", i), Entry{})
	}
	now = now.Add(2 * time.Minute)
	for i := 0; i < 200; i++ {
		c.Put("tok", fmt.Sprintf("g%d", i), Entry{})
	}

	c.mu.Lock()
	size := len(c.entries)
	c.mu.Unlock()
	if size > 1200 {
		t.Errorf("expired entries not swept: %d still stored", size)
	}
	if _, ok := c.Get("tok", "g199"); !ok {
		t.Error("fresh entry missing after sweep")
	}
}

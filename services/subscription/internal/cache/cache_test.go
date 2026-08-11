package cache

import (
	"fmt"
	"testing"
	"time"
)

func TestGetReclaimsDeadEntries(t *testing.T) {
	clock := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	c := New(time.Minute, func() time.Time { return clock })

	c.Put("tok", "base64", Entry{Body: []byte("x")})
	if _, ok := c.Get("tok", "base64"); !ok {
		t.Fatal("want fresh hit")
	}

	clock = clock.Add(2 * time.Minute)
	if _, ok := c.Get("tok", "base64"); ok {
		t.Fatal("want expired miss")
	}
	if len(c.entries) != 0 {
		t.Fatalf("expired entry not reclaimed: %d left", len(c.entries))
	}

	c.Put("tok", "base64", Entry{Body: []byte("x")})
	c.InvalidateAll()
	if _, ok := c.Get("tok", "base64"); ok {
		t.Fatal("want version-bumped miss")
	}
	if len(c.entries) != 0 {
		t.Fatalf("version-evicted entry not reclaimed: %d left", len(c.entries))
	}
}

// The cache is keyed by user tokens, so it must be bounded: memory used to grow
// linearly with every token that ever hit the service.
func TestPutIsBounded(t *testing.T) {
	c := New(time.Minute, nil)
	for i := 0; i < maxEntries+500; i++ {
		c.Put(fmt.Sprintf("tok-%d", i), "base64", Entry{Body: []byte("x")})
	}
	if len(c.entries) > maxEntries {
		t.Fatalf("cache grew past the cap: %d > %d", len(c.entries), maxEntries)
	}
}

// InvalidateToken must kill EVERY cached format for the token — including ones
// that did not exist when the invalidation list was hardcoded. A revoked token
// kept alive by a newly added format would be a security hole.
func TestInvalidateTokenDropsAllFormats(t *testing.T) {
	c := New(time.Minute, nil)
	for _, f := range []string{"base64", "singbox", "clash", "some-future-format"} {
		c.Put("tok", f, Entry{Body: []byte("x")})
	}
	c.Put("other", "base64", Entry{Body: []byte("keep")})

	c.InvalidateToken("tok")

	for _, f := range []string{"base64", "singbox", "clash", "some-future-format"} {
		if _, ok := c.Get("tok", f); ok {
			t.Fatalf("format %q survived InvalidateToken", f)
		}
	}
	if _, ok := c.Get("other", "base64"); !ok {
		t.Fatal("unrelated token was invalidated")
	}
}

package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caspervpn/delivery/internal/channel"
)

// clock is a manually advanced time source for deterministic limiter tests.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func TestLimiterRateLimits(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	l := newLimiter(1 /*rate*/, 3 /*burst*/, time.Second, c.now)

	// Burst of 3 allowed, 4th denied (bucket empty). Vary cmd to bypass antispam.
	cmds := []string{"/a", "/b", "/c"}
	for i, cmd := range cmds {
		if !l.allow(1, cmd) {
			t.Fatalf("burst token %d should be allowed", i)
		}
	}
	if l.allow(1, "/d") {
		t.Fatalf("4th distinct command should be rate-limited")
	}
	// After 1s, one token refills.
	c.add(time.Second)
	if !l.allow(1, "/e") {
		t.Fatalf("token should refill after 1s")
	}
}

func TestLimiterAntispamDropsDuplicates(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	l := newLimiter(100, 100, 5*time.Second, c.now) // rate not the constraint

	if !l.allow(7, "/get") {
		t.Fatalf("first /get should pass")
	}
	if l.allow(7, "/get") {
		t.Fatalf("identical /get within cooldown must be dropped")
	}
	c.add(6 * time.Second) // past cooldown
	if !l.allow(7, "/get") {
		t.Fatalf("/get after cooldown should pass again")
	}
}

// fakeAPI records sent messages.
type fakeAPI struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeAPI) Send(_ context.Context, _ int64, text string) error {
	f.mu.Lock()
	f.sent = append(f.sent, text)
	f.mu.Unlock()
	return nil
}
func (f *fakeAPI) Network() string { return "telegram" }
func (f *fakeAPI) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1]
}
func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// fakeProvider returns a link for known users, ErrNoUser otherwise.
type fakeProvider struct{ known map[int64]Link }

func (p fakeProvider) SubscriptionLink(_ context.Context, uid int64) (Link, error) {
	l, ok := p.known[uid]
	if !ok {
		return Link{}, ErrNoUser
	}
	return l, nil
}

func newTestBot(api BotAPI, prov SubProvider, now func() time.Time) *Bot {
	b, _ := NewBot(api, prov, BotConfig{RatePerSec: 100, Burst: 100, Cooldown: time.Second, Now: now})
	return b
}

func TestBotServesSubscriptionLink(t *testing.T) {
	api := &fakeAPI{}
	prov := fakeProvider{known: map[int64]Link{
		42: {SubscriptionURL: "https://sub.example/s/tok", HappDeepLink: "happ://import?u=..."},
	}}
	c := &clock{t: time.Unix(0, 0)}
	b := newTestBot(api, prov, c.now)

	if err := b.HandleUpdate(context.Background(), Update{ChatID: 42, UserID: 42, Text: "/get"}); err != nil {
		t.Fatalf("HandleUpdate: %v", err)
	}
	msg := api.last()
	if !strings.Contains(msg, "https://sub.example/s/tok") || !strings.Contains(msg, "happ://import") {
		t.Fatalf("reply missing link/deep-link: %q", msg)
	}
}

func TestBotUnknownUserGetsSignupPrompt(t *testing.T) {
	api := &fakeAPI{}
	c := &clock{t: time.Unix(0, 0)}
	b := newTestBot(api, fakeProvider{known: map[int64]Link{}}, c.now)
	_ = b.HandleUpdate(context.Background(), Update{ChatID: 1, UserID: 1, Text: "/get"})
	if api.last() != notRegisteredText {
		t.Fatalf("expected signup prompt, got %q", api.last())
	}
}

func TestBotRateLimitDropsSilently(t *testing.T) {
	api := &fakeAPI{}
	c := &clock{t: time.Unix(0, 0)}
	// burst 1, so the second distinct command in the same instant is dropped.
	b, _ := NewBot(api, fakeProvider{known: map[int64]Link{}}, BotConfig{RatePerSec: 1, Burst: 1, Cooldown: time.Second, Now: c.now})
	_ = b.HandleUpdate(context.Background(), Update{ChatID: 1, UserID: 1, Text: "/start"})
	_ = b.HandleUpdate(context.Background(), Update{ChatID: 1, UserID: 1, Text: "/help"})
	if api.count() != 1 {
		t.Fatalf("expected exactly 1 reply (2nd rate-limited), got %d", api.count())
	}
}

// fakeStore backs the generic channel adapter.
type fakeStore struct{ m map[string][]byte }

func (s fakeStore) Put(_ context.Context, k string, b []byte) error { s.m[k] = b; return nil }
func (s fakeStore) Get(_ context.Context, k string) ([]byte, error) {
	b, ok := s.m[k]
	if !ok {
		return nil, channel.ErrNotFound
	}
	return b, nil
}

func TestTelegramChannelRoundTrip(t *testing.T) {
	ch, err := NewChannel(fakeStore{m: map[string][]byte{}})
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	blob := []byte("signed-blob")
	if err := ch.Publish(context.Background(), "tok", blob); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got, err := ch.Fetch(context.Background(), "tok")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != string(blob) {
		t.Fatalf("round-trip mismatch")
	}
	if ch.Kind() != channel.KindTelegram {
		t.Fatalf("unexpected kind %q", ch.Kind())
	}
}

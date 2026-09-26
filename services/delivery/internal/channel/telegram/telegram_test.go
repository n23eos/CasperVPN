package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/delivery/internal/channel"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func TestLimiterRateLimitsAndAllowsSameUpdateRetry(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	limiter := newLimiter(1, 2, time.Second, c.now)
	if !limiter.allow(1, "/a", 10) || !limiter.allow(1, "/b", 11) {
		t.Fatal("initial burst should be allowed")
	}
	if limiter.allow(1, "/c", 12) {
		t.Fatal("third distinct update should be rate limited")
	}
	if !limiter.allow(1, "/b", 11) {
		t.Fatal("retry of the same durable update must bypass cooldown")
	}
	c.add(time.Second)
	if !limiter.allow(1, "/c", 12) {
		t.Fatal("token should refill")
	}
}

type sentMessage struct {
	chatID int64
	text   string
}

type fakeAPI struct {
	mu       sync.Mutex
	sent     []sentMessage
	err      error
	failures int
}

func (f *fakeAPI) Send(_ context.Context, chatID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures > 0 {
		f.failures--
		return errors.New("temporary send failure")
	}
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, sentMessage{chatID: chatID, text: text})
	return nil
}
func (f *fakeAPI) Network() string { return "telegram" }
func (f *fakeAPI) last() sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return sentMessage{}
	}
	return f.sent[len(f.sent)-1]
}
func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

type fakeOnboarding struct {
	mu          sync.Mutex
	ensured     []int64
	invoiceUser []int64
	linkUser    []int64
	link        Link
	invoice     Invoice
	linkErrs    []error
}

func (f *fakeOnboarding) EnsureUser(_ context.Context, senderID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, senderID)
	return nil
}

func (f *fakeOnboarding) CreateInvoice(_ context.Context, senderID, _ int64, _ contracts.SubscriptionPlan, _ string) (Invoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invoiceUser = append(f.invoiceUser, senderID)
	return f.invoice, nil
}

func (f *fakeOnboarding) SubscriptionLink(_ context.Context, senderID int64) (Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkUser = append(f.linkUser, senderID)
	if len(f.linkErrs) > 0 {
		err := f.linkErrs[0]
		f.linkErrs = f.linkErrs[1:]
		return Link{}, err
	}
	return f.link, nil
}

func newTestBot(t *testing.T, api BotAPI, onboarding Onboarding, now func() time.Time) *Bot {
	t.Helper()
	bot, err := NewBot(api, onboarding, BotConfig{
		RatePerSec: 100, Burst: 100, Cooldown: time.Minute,
		DefaultPlan: contracts.SubscriptionPlanBasic, DefaultCurrency: "XMR", Now: now,
	})
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	return bot
}

func privateUpdate(id, userID int64, text string) Update {
	return Update{UpdateID: id, ChatID: userID, ChatType: "private", UserID: userID, Text: text}
}

func TestBotStartPayGetUsesSenderIdentity(t *testing.T) {
	api := &fakeAPI{}
	onboarding := &fakeOnboarding{
		link:    Link{SubscriptionURL: "https://sub.example/sub/stable"},
		invoice: Invoice{ID: "inv-1", Amount: "1", Currency: "XMR", PayAddress: "address", ExpiresAt: time.Unix(100, 0)},
	}
	bot := newTestBot(t, api, onboarding, time.Now)
	for _, update := range []Update{
		privateUpdate(1, 42, "/start another-user"),
		privateUpdate(2, 42, "/pay unlimited XMR"),
		privateUpdate(3, 42, "/get 999999"),
	} {
		if err := bot.HandleUpdate(context.Background(), update); err != nil {
			t.Fatalf("HandleUpdate(%q): %v", update.Text, err)
		}
	}
	if len(onboarding.ensured) != 1 || onboarding.ensured[0] != 42 {
		t.Fatalf("start identity = %v", onboarding.ensured)
	}
	if len(onboarding.invoiceUser) != 1 || onboarding.invoiceUser[0] != 42 {
		t.Fatalf("invoice identity = %v", onboarding.invoiceUser)
	}
	if len(onboarding.linkUser) != 1 || onboarding.linkUser[0] != 42 {
		t.Fatalf("link identity = %v", onboarding.linkUser)
	}
	if got := api.last(); got.chatID != 42 || !strings.Contains(got.text, "/sub/stable") {
		t.Fatalf("unexpected get response: %+v", got)
	}
}

func TestBotIgnoresGroupAndMissingSender(t *testing.T) {
	api := &fakeAPI{}
	onboarding := &fakeOnboarding{}
	bot := newTestBot(t, api, onboarding, time.Now)
	updates := []Update{
		{UpdateID: 1, ChatID: -100, ChatType: "group", UserID: 42, Text: "/get"},
		{UpdateID: 2, ChatID: 42, ChatType: "private", Text: "/get"},
		{UpdateID: 3, ChatID: 99, ChatType: "private", UserID: 42, Text: "/get"},
	}
	for _, update := range updates {
		if err := bot.HandleUpdate(context.Background(), update); err != nil {
			t.Fatalf("HandleUpdate: %v", err)
		}
	}
	if api.count() != 0 || len(onboarding.linkUser) != 0 {
		t.Fatalf("non-private updates must have no effects")
	}
}

func TestBotRetriesSameUpdateAfterTemporarySendFailure(t *testing.T) {
	api := &fakeAPI{failures: 1}
	onboarding := &fakeOnboarding{
		link: Link{SubscriptionURL: "https://sub.example/sub/stable"},
	}
	bot := newTestBot(t, api, onboarding, time.Now)
	update := privateUpdate(7, 42, "/get")
	if err := bot.HandleUpdate(context.Background(), update); err == nil {
		t.Fatal("first send should fail")
	}
	if err := bot.HandleUpdate(context.Background(), update); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(onboarding.linkUser) != 2 {
		t.Fatalf("same update should be retried, calls = %d", len(onboarding.linkUser))
	}
}

func TestBotRejectsImpersonationExtraPayArgument(t *testing.T) {
	api := &fakeAPI{}
	onboarding := &fakeOnboarding{}
	bot := newTestBot(t, api, onboarding, time.Now)
	if err := bot.HandleUpdate(context.Background(), privateUpdate(1, 42, "/pay basic XMR 999")); err != nil {
		t.Fatalf("HandleUpdate: %v", err)
	}
	if len(onboarding.invoiceUser) != 0 || api.last().text != payUsageText {
		t.Fatal("extra identity-like argument must not create an invoice")
	}
}

type fakeStore struct{ m map[string][]byte }

func (s fakeStore) Put(_ context.Context, key string, blob []byte) error { s.m[key] = blob; return nil }
func (s fakeStore) Get(_ context.Context, key string) ([]byte, error) {
	blob, ok := s.m[key]
	if !ok {
		return nil, channel.ErrNotFound
	}
	return blob, nil
}

func TestTelegramChannelRoundTrip(t *testing.T) {
	ch, err := NewChannel(fakeStore{m: map[string][]byte{}})
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	if err := ch.Publish(context.Background(), "tok", []byte("signed-blob")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got, err := ch.Fetch(context.Background(), "tok")
	if err != nil || string(got) != "signed-blob" {
		t.Fatalf("round trip = %q, %v", got, err)
	}
	if ch.Kind() != channel.KindTelegram {
		t.Fatalf("kind = %q", ch.Kind())
	}
}

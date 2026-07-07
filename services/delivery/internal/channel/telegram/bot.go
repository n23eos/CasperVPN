package telegram

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Link is what the bot hands a user: their subscription URL and a Happ deep-link
// that imports it in one tap. Both are derived from config/DB per user — no URL
// or host is hardcoded (CLAUDE.md [ANTI-BLOCK] rule 5).
type Link struct {
	SubscriptionURL string
	HappDeepLink    string
}

// SubProvider resolves a messenger user to their subscription link. Implemented by
// a subscription-service client; injected so this package holds no business logic
// or hardcoded endpoint. ErrNoUser signals an unregistered user.
type SubProvider interface {
	SubscriptionLink(ctx context.Context, userID int64) (Link, error)
}

// ErrNoUser is returned by a SubProvider for an unknown/unregistered user.
var ErrNoUser = errors.New("telegram: no subscription for user")

// BotConfig holds the antispam/rate knobs and reply copy. Defaults are applied by
// NewBot when zero.
type BotConfig struct {
	RatePerSec int
	Burst      int
	Cooldown   time.Duration
	// Now is injectable for deterministic tests; nil => time.Now.
	Now func() time.Time
}

// Bot serves subscription links over one messenger network via a BotAPI.
type Bot struct {
	api      BotAPI
	provider SubProvider
	lim      *limiter
}

// Default rate/antispam tunables (named — no magic numbers).
const (
	defaultRatePerSec = 1
	defaultBurst      = 5
	defaultCooldown   = 3 * time.Second
)

// NewBot builds a bot. api and provider are required.
func NewBot(api BotAPI, provider SubProvider, cfg BotConfig) (*Bot, error) {
	if api == nil || provider == nil {
		return nil, errors.New("telegram: api and provider required")
	}
	if cfg.RatePerSec <= 0 {
		cfg.RatePerSec = defaultRatePerSec
	}
	if cfg.Burst <= 0 {
		cfg.Burst = defaultBurst
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = defaultCooldown
	}
	return &Bot{
		api:      api,
		provider: provider,
		lim:      newLimiter(cfg.RatePerSec, cfg.Burst, cfg.Cooldown, cfg.Now),
	}, nil
}

// command extracts the leading command word ("/get foo" -> "/get"), lowercased.
func command(text string) string {
	f := strings.Fields(strings.TrimSpace(text))
	if len(f) == 0 {
		return ""
	}
	return strings.ToLower(f[0])
}

// HandleUpdate processes one inbound update: rate-limit + antispam, then dispatch.
// It replies via the BotAPI and returns any send error (nil when silently dropped
// by the limiter — dropping is not an error).
func (b *Bot) HandleUpdate(ctx context.Context, u Update) error {
	cmd := command(u.Text)

	if !b.lim.allow(u.UserID, cmd) {
		// Rate-limited or duplicate: drop silently so we don't amplify a flood.
		return nil
	}

	switch cmd {
	case "/start", "/help":
		return b.api.Send(ctx, u.ChatID, helpText)
	case "/get", "/sub", "/subscription":
		return b.sendSubscription(ctx, u)
	default:
		return b.api.Send(ctx, u.ChatID, helpText)
	}
}

func (b *Bot) sendSubscription(ctx context.Context, u Update) error {
	link, err := b.provider.SubscriptionLink(ctx, u.UserID)
	if err != nil {
		if errors.Is(err, ErrNoUser) {
			return b.api.Send(ctx, u.ChatID, notRegisteredText)
		}
		return b.api.Send(ctx, u.ChatID, tempErrorText)
	}
	msg := "Your subscription link:\n" + link.SubscriptionURL
	if link.HappDeepLink != "" {
		msg += "\n\nOne-tap import (Happ):\n" + link.HappDeepLink
	}
	return b.api.Send(ctx, u.ChatID, msg)
}

// Static reply copy. No secrets, no hardcoded endpoints.
const (
	helpText          = "Send /get to receive your current subscription link and one-tap Happ import. Links rotate automatically — always fetch a fresh one with /get."
	notRegisteredText = "No active subscription found for this account. Complete signup first, then send /get."
	tempErrorText     = "Temporarily unable to fetch your link. Please try /get again in a moment."
)

package telegram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/caspervpn/contracts"
)

// Link is the stable personal subscription URL returned by control-plane.
type Link struct {
	SubscriptionURL string
	HappDeepLink    string
}

// Invoice is safe payment information returned by billing.
type Invoice struct {
	ID          string
	Provider    string
	PayAddress  string
	CheckoutURL string
	Amount      string
	Currency    string
	ExpiresAt   time.Time
}

var (
	ErrNoSubscription = errors.New("telegram: no subscription")
	ErrNotEligible     = errors.New("telegram: subscription not eligible")
	ErrTemporary       = errors.New("telegram: temporary onboarding failure")
)

// Onboarding is the self-service business surface used by the bot. Every method
// binds identity to senderID supplied by Telegram, never to command arguments.
type Onboarding interface {
	EnsureUser(ctx context.Context, senderID int64) error
	CreateInvoice(ctx context.Context, senderID, updateID int64, plan contracts.SubscriptionPlan, currency string) (Invoice, error)
	SubscriptionLink(ctx context.Context, senderID int64) (Link, error)
}

type BotConfig struct {
	RatePerSec      int
	Burst           int
	Cooldown        time.Duration
	DefaultPlan     contracts.SubscriptionPlan
	DefaultCurrency string
	Now             func() time.Time
}

type Bot struct {
	api        BotAPI
	onboarding Onboarding
	lim        *limiter
	plan       contracts.SubscriptionPlan
	currency   string
}

const (
	defaultRatePerSec = 1
	defaultBurst      = 5
	defaultCooldown   = 3 * time.Second
)

var currencyPattern = regexp.MustCompile(`^[A-Z0-9_]{2,16}$`)

func NewBot(api BotAPI, onboarding Onboarding, cfg BotConfig) (*Bot, error) {
	if api == nil || onboarding == nil {
		return nil, errors.New("telegram: api and onboarding required")
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
	if !cfg.DefaultPlan.Valid() {
		cfg.DefaultPlan = contracts.SubscriptionPlanBasic
	}
	cfg.DefaultCurrency = strings.ToUpper(strings.TrimSpace(cfg.DefaultCurrency))
	if !currencyPattern.MatchString(cfg.DefaultCurrency) {
		return nil, errors.New("telegram: default currency is invalid")
	}
	return &Bot{
		api:        api,
		onboarding: onboarding,
		lim:        newLimiter(cfg.RatePerSec, cfg.Burst, cfg.Cooldown, cfg.Now),
		plan:       cfg.DefaultPlan,
		currency:   cfg.DefaultCurrency,
	}, nil
}

func command(text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return ""
	}
	cmd := strings.ToLower(fields[0])
	if i := strings.IndexByte(cmd, '@'); i > 0 {
		cmd = cmd[:i]
	}
	return cmd
}

func (b *Bot) HandleUpdate(ctx context.Context, update Update) error {
	// Only private messages with an explicit Telegram sender are accepted.
	// Group chat IDs and text payloads cannot impersonate another account.
	if update.ChatType != "private" || update.UserID <= 0 || update.ChatID == 0 {
		return nil
	}
	cmd := command(update.Text)
	if !b.lim.allow(update.UserID, cmd, update.UpdateID) {
		return nil
	}

	switch cmd {
	case "/start", "/help":
		if err := b.onboarding.EnsureUser(ctx, update.UserID); err != nil {
			return b.sendTemporaryError(ctx, update.ChatID)
		}
		return b.api.Send(ctx, update.ChatID, helpText)
	case "/pay":
		return b.sendInvoice(ctx, update)
	case "/get", "/sub", "/subscription":
		return b.sendSubscription(ctx, update)
	default:
		return b.api.Send(ctx, update.ChatID, helpText)
	}
}

func (b *Bot) sendInvoice(ctx context.Context, update Update) error {
	plan, currency, ok := b.paymentArgs(update.Text)
	if !ok {
		return b.api.Send(ctx, update.ChatID, payUsageText)
	}
	invoice, err := b.onboarding.CreateInvoice(ctx, update.UserID, update.UpdateID, plan, currency)
	if err != nil {
		return b.sendTemporaryError(ctx, update.ChatID)
	}
	paymentTarget := invoice.CheckoutURL
	if paymentTarget == "" {
		paymentTarget = invoice.PayAddress
	}
	message := fmt.Sprintf("Invoice %s\nAmount: %s %s\nPay: %s\nExpires: %s",
		invoice.ID, invoice.Amount, invoice.Currency, paymentTarget,
		invoice.ExpiresAt.UTC().Format(time.RFC3339))
	return b.api.Send(ctx, update.ChatID, message)
}

func (b *Bot) paymentArgs(text string) (contracts.SubscriptionPlan, string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) > 3 {
		return "", "", false
	}
	plan := b.plan
	currency := b.currency
	if len(fields) >= 2 {
		plan = contracts.SubscriptionPlan(strings.ToLower(fields[1]))
	}
	if len(fields) == 3 {
		currency = strings.ToUpper(fields[2])
	}
	return plan, currency, plan.Valid() && currencyPattern.MatchString(currency)
}

func (b *Bot) sendSubscription(ctx context.Context, update Update) error {
	link, err := b.onboarding.SubscriptionLink(ctx, update.UserID)
	if err != nil {
		if errors.Is(err, ErrNoSubscription) || errors.Is(err, ErrNotEligible) {
			return b.api.Send(ctx, update.ChatID, noSubscriptionText)
		}
		return b.sendTemporaryError(ctx, update.ChatID)
	}
	message := "Your subscription link:\n" + link.SubscriptionURL
	if link.HappDeepLink != "" {
		message += "\n\nOne-tap import (Happ):\n" + link.HappDeepLink
	}
	return b.api.Send(ctx, update.ChatID, message)
}

func (b *Bot) sendTemporaryError(ctx context.Context, chatID int64) error {
	if err := b.api.Send(ctx, chatID, tempErrorText); err != nil {
		return err
	}
	return ErrTemporary
}

const (
	helpText           = "Commands:\n/start - create or restore your account\n/pay [basic|unlimited] [currency] - create a payment invoice\n/get - receive your current subscription link"
	payUsageText       = "Usage: /pay [basic|unlimited] [currency]"
	noSubscriptionText = "No eligible subscription is available. Send /pay to create an invoice, then use /get after payment is confirmed."
	tempErrorText      = "The service is temporarily unavailable. Please try again."
)

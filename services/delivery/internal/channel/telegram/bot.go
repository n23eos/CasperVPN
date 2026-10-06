package telegram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/caspervpn/contracts"
)

type Link struct {
	SubscriptionURL string
	HappDeepLink    string
}

type Invoice struct {
	ID          string
	Provider    string
	PayAddress  string
	CheckoutURL string
	Amount      string
	Currency    string
	ExpiresAt   time.Time
}

// Plan is one live billing catalog offer. Prices remain decimal strings so the
// bot never invents or rounds money values.
type Plan struct {
	ID              contracts.SubscriptionPlan
	DurationSeconds int64
	GraceSeconds    int64
	Prices          map[string]string
}

type LatestInvoice struct {
	ID        string
	Plan      contracts.SubscriptionPlan
	Status    string
	Amount    string
	Currency  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// AccountStatus combines billing state with control-plane authority. Eligible
// is true only when control-plane confirms current access.
type AccountStatus struct {
	UserStatus   contracts.UserStatus
	Invoice      *LatestInvoice
	Subscription *contracts.Subscription
	Eligible     bool
}

var (
	ErrNoSubscription     = errors.New("telegram: no subscription")
	ErrNotEligible        = errors.New("telegram: subscription not eligible")
	ErrAccountSuspended   = errors.New("telegram: account suspended")
	ErrUnsupportedCatalog = errors.New("telegram: unsupported catalog selection")
	ErrRateLimited        = errors.New("telegram: upstream rate limited")
)

// Onboarding always binds account reads and writes to the Telegram sender ID.
type Onboarding interface {
	EnsureUser(ctx context.Context, senderID int64) error
	Catalog(ctx context.Context) ([]Plan, error)
	CreateInvoice(ctx context.Context, senderID, updateID int64, plan contracts.SubscriptionPlan, currency string) (Invoice, error)
	Status(ctx context.Context, senderID int64) (AccountStatus, error)
	SubscriptionLink(ctx context.Context, senderID int64) (Link, error)
}

// UserTracker remembers users who privately interacted with this bot.
type UserTracker interface {
	RecordUser(ctx context.Context, telegramID int64) error
}

type BotConfig struct {
	RatePerSec      int
	Burst           int
	Cooldown        time.Duration
	DefaultPlan     contracts.SubscriptionPlan
	DefaultCurrency string
	SetupURL        string
	SupportURL      string
	Tracker         UserTracker
	Now             func() time.Time
}

type Bot struct {
	api        BotAPI
	onboarding Onboarding
	lim        *limiter
	plan       contracts.SubscriptionPlan
	currency   string
	setupURL   string
	supportURL string
	tracker    UserTracker
}

const (
	defaultRatePerSec = 1
	defaultBurst      = 5
	defaultCooldown   = 3 * time.Second
	connectButton     = "Подключиться"
	statusButton      = "Моя подписка"
	renewButton       = "Продлить"
	helpButton        = "Помощь"
	planPrefix        = "Выбрать: "
)

var (
	currencyPattern = regexp.MustCompile(`^[A-Z0-9_]{2,16}$`)
	mainKeyboard    = [][]string{{connectButton, statusButton}, {renewButton, helpButton}}
)

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
		api: api, onboarding: onboarding,
		lim:  newLimiter(cfg.RatePerSec, cfg.Burst, cfg.Cooldown, cfg.Now),
		plan: cfg.DefaultPlan, currency: cfg.DefaultCurrency,
		setupURL: strings.TrimSpace(cfg.SetupURL), supportURL: strings.TrimSpace(cfg.SupportURL), tracker: cfg.Tracker,
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
	if update.ChatType != "private" || update.UserID <= 0 || update.ChatID != update.UserID {
		return nil
	}
	if b.tracker != nil {
		if err := b.tracker.RecordUser(ctx, update.UserID); err != nil {
			return err
		}
	}
	cmd := command(update.Text)
	if !b.lim.allow(update.UserID, cmd, update.UpdateID) {
		return nil
	}
	switch {
	case cmd == "/start" || cmd == "/help" || update.Text == helpButton:
		if err := b.onboarding.EnsureUser(ctx, update.UserID); err != nil {
			return b.sendKnownError(ctx, update.ChatID, err)
		}
		return b.sendMenu(ctx, update.ChatID, helpText)
	case cmd == "/status" || update.Text == statusButton:
		return b.sendStatus(ctx, update)
	case cmd == "/pay" && len(strings.Fields(update.Text)) == 1:
		return b.sendPlans(ctx, update.ChatID)
	case cmd == "/pay":
		return b.sendInvoiceCommand(ctx, update)
	case update.Text == renewButton:
		return b.sendPlans(ctx, update.ChatID)
	case cmd == "/get" || cmd == "/sub" || cmd == "/subscription" || update.Text == connectButton:
		return b.sendSubscription(ctx, update)
	case strings.HasPrefix(update.Text, planPrefix):
		return b.sendSelectedInvoice(ctx, update)
	default:
		return b.sendMenu(ctx, update.ChatID, unknownText)
	}
}

func (b *Bot) sendPlans(ctx context.Context, chatID int64) error {
	plans, err := b.onboarding.Catalog(ctx)
	if err != nil {
		return b.sendKnownError(ctx, chatID, err)
	}
	buttons := catalogButtons(plans)
	if len(buttons) == 0 {
		return b.sendMenu(ctx, chatID, emptyCatalogText)
	}
	keyboard := make([][]string, 0, len(buttons)+len(mainKeyboard))
	for _, button := range buttons {
		keyboard = append(keyboard, []string{button.label})
	}
	keyboard = append(keyboard, mainKeyboard...)
	return b.send(ctx, chatID, Message{Text: renderCatalog(plans), ReplyKeyboard: keyboard})
}

func (b *Bot) sendSelectedInvoice(ctx context.Context, update Update) error {
	plans, err := b.onboarding.Catalog(ctx)
	if err != nil {
		return b.sendKnownError(ctx, update.ChatID, err)
	}
	for _, button := range catalogButtons(plans) {
		if update.Text == button.label {
			return b.createAndSendInvoice(ctx, update, button.plan, button.currency)
		}
	}
	return b.sendMenu(ctx, update.ChatID, stalePlanText)
}

func (b *Bot) sendInvoiceCommand(ctx context.Context, update Update) error {
	plan, currency, ok := b.paymentArgs(update.Text)
	if !ok {
		return b.sendMenu(ctx, update.ChatID, payUsageText)
	}
	return b.createAndSendInvoice(ctx, update, plan, currency)
}

func (b *Bot) createAndSendInvoice(ctx context.Context, update Update, plan contracts.SubscriptionPlan, currency string) error {
	invoice, err := b.onboarding.CreateInvoice(ctx, update.UserID, update.UpdateID, plan, currency)
	if err != nil {
		return b.sendKnownError(ctx, update.ChatID, err)
	}
	paymentTarget := invoice.CheckoutURL
	if paymentTarget == "" {
		paymentTarget = invoice.PayAddress
	}
	message := fmt.Sprintf("Счёт %s\nСумма: %s %s\nОплата: %s\nСчёт действует до: %s\n\nПосле подтверждения проверьте «Моя подписка».", invoice.ID, invoice.Amount, invoice.Currency, paymentTarget, formatTime(invoice.ExpiresAt))
	return b.sendMenu(ctx, update.ChatID, message)
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

func (b *Bot) sendStatus(ctx context.Context, update Update) error {
	status, err := b.onboarding.Status(ctx, update.UserID)
	if err != nil {
		return b.sendKnownError(ctx, update.ChatID, err)
	}
	return b.sendMenu(ctx, update.ChatID, renderStatus(status))
}

func (b *Bot) sendSubscription(ctx context.Context, update Update) error {
	link, err := b.onboarding.SubscriptionLink(ctx, update.UserID)
	if err != nil {
		switch {
		case errors.Is(err, ErrAccountSuspended):
			return b.sendMenu(ctx, update.ChatID, suspendedText)
		case errors.Is(err, ErrNoSubscription), errors.Is(err, ErrNotEligible):
			return b.sendMenu(ctx, update.ChatID, noSubscriptionText)
		default:
			return b.sendKnownError(ctx, update.ChatID, err)
		}
	}
	message := "Ваша персональная ссылка подписки:\n" + link.SubscriptionURL + "\n\nКак подключить:\n1. Установите Happ на устройство.\n2. Добавьте подписку по ссылке ниже.\n3. Обновите подписку в приложении и выберите доступный сервер."
	if link.HappDeepLink != "" {
		message += "\n\nИмпорт в Happ:\n" + link.HappDeepLink
	}
	if b.setupURL != "" {
		message += "\n\nИнструкция для устройств:\n" + b.setupURL
	}
	if b.supportURL != "" {
		message += "\n\nПоддержка:\n" + b.supportURL
	}
	return b.sendMenu(ctx, update.ChatID, message)
}

func (b *Bot) sendKnownError(ctx context.Context, chatID int64, err error) error {
	switch {
	case errors.Is(err, ErrRateLimited):
		return b.sendMenu(ctx, chatID, rateLimitText)
	case errors.Is(err, ErrUnsupportedCatalog):
		return b.sendMenu(ctx, chatID, unsupportedText)
	default:
		return b.sendMenu(ctx, chatID, tempErrorText)
	}
}

func (b *Bot) sendMenu(ctx context.Context, chatID int64, text string) error {
	return b.send(ctx, chatID, Message{Text: text, ReplyKeyboard: mainKeyboard})
}

func (b *Bot) send(ctx context.Context, chatID int64, message Message) error {
	if sender, ok := b.api.(messageSender); ok {
		return sender.SendMessage(ctx, chatID, message)
	}
	return b.api.Send(ctx, chatID, message.Text)
}

type catalogButton struct {
	label    string
	plan     contracts.SubscriptionPlan
	currency string
}

func catalogButtons(plans []Plan) []catalogButton {
	buttons := make([]catalogButton, 0)
	for _, plan := range plans {
		if !plan.ID.Valid() || plan.DurationSeconds <= 0 {
			continue
		}
		currencies := make([]string, 0, len(plan.Prices))
		prices := make(map[string]string, len(plan.Prices))
		for currency, price := range plan.Prices {
			currency = strings.ToUpper(strings.TrimSpace(currency))
			if currencyPattern.MatchString(currency) && strings.TrimSpace(price) != "" {
				currencies = append(currencies, currency)
				prices[currency] = strings.TrimSpace(price)
			}
		}
		sort.Strings(currencies)
		for _, currency := range currencies {
			buttons = append(buttons, catalogButton{
				label: fmt.Sprintf("%s%s, %s: %s %s", planPrefix, planName(plan.ID), durationLabel(plan.DurationSeconds), prices[currency], currency),
				plan:  plan.ID, currency: currency,
			})
		}
	}
	sort.Slice(buttons, func(i, j int) bool { return buttons[i].label < buttons[j].label })
	return buttons
}

func planName(plan contracts.SubscriptionPlan) string {
	switch plan {
	case contracts.SubscriptionPlanBasic:
		return "Базовый"
	case contracts.SubscriptionPlanUnlimited:
		return "Безлимитный"
	default:
		return "Тариф"
	}
}

func durationLabel(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d дн.", int64(d/(24*time.Hour)))
	}
	if d%time.Hour == 0 {
		return fmt.Sprintf("%d ч.", int64(d/time.Hour))
	}
	return fmt.Sprintf("%d мин.", int64(d/time.Minute))
}

func renderCatalog(plans []Plan) string {
	ordered := append([]Plan(nil), plans...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	lines := []string{choosePlanText}
	for _, plan := range ordered {
		if !plan.ID.Valid() || plan.DurationSeconds <= 0 {
			continue
		}
		grace := "без льготного периода"
		if plan.GraceSeconds > 0 {
			grace = "льготный период " + durationLabel(plan.GraceSeconds)
		}
		lines = append(lines, "", planName(plan.ID)+": "+durationLabel(plan.DurationSeconds)+", "+grace)
		currencies := make([]string, 0, len(plan.Prices))
		prices := make(map[string]string, len(plan.Prices))
		for currency, price := range plan.Prices {
			currency = strings.ToUpper(strings.TrimSpace(currency))
			if currencyPattern.MatchString(currency) && strings.TrimSpace(price) != "" {
				currencies = append(currencies, currency)
				prices[currency] = strings.TrimSpace(price)
			}
		}
		sort.Strings(currencies)
		for _, currency := range currencies {
			lines = append(lines, prices[currency]+" "+currency)
		}
	}
	return strings.Join(lines, "\n")
}

func renderStatus(status AccountStatus) string {
	lines := []string{"Состояние аккаунта"}
	blocked := false
	switch status.UserStatus {
	case contracts.UserStatusSuspended:
		blocked = true
		lines = append(lines, "Доступ приостановлен. Обратитесь в поддержку.")
	case contracts.UserStatusBanned:
		blocked = true
		lines = append(lines, "Доступ заблокирован.")
	case contracts.UserStatusExpired:
		lines = append(lines, "Активной подписки нет.")
	}
	ended := status.UserStatus == contracts.UserStatusExpired || subscriptionEnded(status.Subscription, time.Now())
	if status.Invoice != nil {
		lines = append(lines, invoiceStatusLine(*status.Invoice, status.Eligible, ended, blocked))
	}
	if status.Subscription == nil {
		lines = append(lines, "Подписки пока нет.")
		return strings.Join(lines, "\n")
	}
	sub := status.Subscription
	lines = append(lines, "Тариф: "+planName(sub.Plan))
	if sub.ExpiresAt != nil {
		lines = append(lines, "Оплачено до: "+formatTime(*sub.ExpiresAt))
	}
	if sub.GraceUntil != nil && (sub.ExpiresAt == nil || sub.GraceUntil.After(*sub.ExpiresAt)) {
		lines = append(lines, "Льготный период до: "+formatTime(*sub.GraceUntil))
	}
	if status.Eligible {
		lines = append(lines, "Доступ активен.")
	} else if ended {
		lines = append(lines, "Срок доступа закончился. Откройте «Продлить».")
	} else {
		lines = append(lines, "Доступ пока не активирован. Проверьте статус позже.")
	}
	return strings.Join(lines, "\n")
}

func invoiceStatusLine(invoice LatestInvoice, eligible, ended, blocked bool) string {
	switch invoice.Status {
	case "pending":
		return "Платёж: ожидает подтверждения."
	case "settled":
		if eligible {
			return "Платёж: подтверждён."
		}
		if blocked {
			return "Платёж: подтверждён. Доступ ограничен состоянием аккаунта."
		}
		if ended {
			return "Платёж был подтверждён, но срок доступа закончился. Продлите подписку."
		}
		return "Платёж подтверждён, доступ пока активируется."
	case "expired":
		return "Платёж: срок счёта истёк. Создайте новый счёт."
	case "invalid":
		return "Платёж: счёт недействителен. Создайте новый счёт."
	default:
		return "Платёж: неизвестное состояние. Обновите статус позже."
	}
}

func subscriptionEnded(subscription *contracts.Subscription, now time.Time) bool {
	if subscription == nil {
		return false
	}
	switch subscription.Status {
	case contracts.SubscriptionStatusExpired, contracts.SubscriptionStatusCanceled:
		return true
	}
	until := subscription.AccessUntil()
	return until != nil && !until.After(now)
}

func formatTime(value time.Time) string { return value.UTC().Format("02.01.2006 15:04 UTC") }

const (
	helpText           = "CasperVPN использует внешнее приложение Happ. Выберите действие в меню.\n\nКоманды: /pay, /status, /get, /help. Старый формат /pay [basic|unlimited] [currency] также работает."
	unknownText        = "Не понял сообщение. Выберите действие в меню."
	choosePlanText     = "Выберите тариф и валюту. Цены и срок загружены из текущего каталога."
	emptyCatalogText   = "Сейчас нет доступных тарифов. Попробуйте позже."
	stalePlanText      = "Этот вариант уже изменился. Нажмите «Продлить» и выберите актуальный тариф."
	payUsageText       = "Формат команды: /pay [basic|unlimited] [currency]. Или выберите тариф через «Продлить»."
	noSubscriptionText = "Доступной подписки пока нет. Проверьте «Моя подписка» или создайте счёт через «Продлить»."
	suspendedText      = "Доступ приостановлен. Ссылка подключения недоступна. Обратитесь в поддержку."
	rateLimitText      = "Слишком много запросов. Подождите немного и повторите."
	unsupportedText    = "Выбранная валюта или тариф больше не поддерживается. Откройте «Продлить» и выберите новый вариант."
	tempErrorText      = "Сервис временно недоступен. Попробуйте ещё раз позже."
)

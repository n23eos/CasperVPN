package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/caspervpn/contracts"
)

// NotificationStore provides bounded, durably paginated users and a persistent
// pending queue. Successful delivery is marked only after Send returns. A crash
// between send and mark can still produce a duplicate.
type NotificationStore interface {
	NotificationUsers(ctx context.Context, limit int) ([]int64, error)
	ReconcileNotifications(ctx context.Context, telegramID int64, current []Notification, allowActivation bool) error
	PendingNotifications(ctx context.Context, telegramID int64, limit int) ([]Notification, error)
	MarkNotificationSent(ctx context.Context, telegramID int64, eventKey string) error
}

type Notification struct {
	EventKey string
	Kind     string
	Text     string
}

type NotifierConfig struct {
	Interval  time.Duration
	Window    time.Duration
	BatchSize int
	Now       func() time.Time
}

type Notifier struct {
	api        BotAPI
	onboarding Onboarding
	store      NotificationStore
	interval   time.Duration
	window     time.Duration
	batchSize  int
	now        func() time.Time
	running    atomic.Bool
}

func NewNotifier(api BotAPI, onboarding Onboarding, store NotificationStore, cfg NotifierConfig) (*Notifier, error) {
	if api == nil || onboarding == nil || store == nil {
		return nil, errors.New("telegram notifier: api, onboarding and store required")
	}
	if cfg.Interval <= 0 || cfg.Window <= 0 || cfg.BatchSize <= 0 {
		return nil, errors.New("telegram notifier: positive interval, window and batch size required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Notifier{
		api: api, onboarding: onboarding, store: store,
		interval: cfg.Interval, window: cfg.Window, batchSize: cfg.BatchSize, now: cfg.Now,
	}, nil
}

func (n *Notifier) Run(ctx context.Context) {
	_ = n.RunOnce(ctx)
	ticker := time.NewTicker(n.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = n.RunOnce(ctx)
		}
	}
}

// RunOnce handles at most BatchSize users. NotificationUsers persists its page
// cursor so repeated runs and restarts eventually visit users beyond one batch.
func (n *Notifier) RunOnce(ctx context.Context) error {
	if !n.running.CompareAndSwap(false, true) {
		return nil
	}
	defer n.running.Store(false)
	users, err := n.store.NotificationUsers(ctx, n.batchSize)
	if err != nil {
		return err
	}
	var errs []error
	for _, telegramID := range users {
		status, err := n.onboarding.Status(ctx, telegramID)
		if err != nil {
			errs = append(errs, fmt.Errorf("status %d: %w", telegramID, err))
			continue
		}
		current := notificationEvents(status, n.now().UTC(), n.window)
		allowActivation := status.UserStatus == contracts.UserStatusActive && status.Eligible
		if err := n.store.ReconcileNotifications(ctx, telegramID, current, allowActivation); err != nil {
			errs = append(errs, fmt.Errorf("reconcile notifications %d: %w", telegramID, err))
			continue
		}
		pending, err := n.store.PendingNotifications(ctx, telegramID, n.batchSize)
		if err != nil {
			errs = append(errs, fmt.Errorf("read notifications %d: %w", telegramID, err))
			continue
		}
		for _, event := range pending {
			if err := n.api.Send(ctx, telegramID, event.Text); err != nil {
				errs = append(errs, fmt.Errorf("send notification %d: %w", telegramID, err))
				continue
			}
			if err := n.store.MarkNotificationSent(ctx, telegramID, event.EventKey); err != nil {
				errs = append(errs, fmt.Errorf("mark notification %d: %w", telegramID, err))
			}
		}
	}
	return errors.Join(errs...)
}

func notificationEvents(status AccountStatus, now time.Time, window time.Duration) []Notification {
	if status.UserStatus != contracts.UserStatusActive || !status.Eligible || status.Subscription == nil {
		return nil
	}
	sub := status.Subscription
	events := make([]Notification, 0, 2)
	if status.Invoice != nil && status.Invoice.Status == string(contracts.BillingPaymentSettled) {
		events = append(events, Notification{
			EventKey: "activation:" + status.Invoice.ID + ":" + sub.ID,
			Kind:     "activation",
			Text:     "Оплата подтверждена, и доступ уже активирован. Откройте «Подключиться», чтобы получить персональную ссылку.",
		})
	}
	if sub.ExpiresAt == nil {
		return events
	}
	expiresAt := sub.ExpiresAt.UTC()
	if expiresAt.After(now) && !expiresAt.After(now.Add(window)) {
		events = append(events, Notification{
			EventKey: "expiry:" + sub.ID + ":" + strconv.FormatInt(expiresAt.Unix(), 10),
			Kind:     "expiry",
			Text:     "Подписка действует до " + formatTime(expiresAt) + ". Для продления откройте «Продлить».",
		})
		return events
	}
	if !expiresAt.After(now) && sub.GraceUntil != nil {
		graceUntil := sub.GraceUntil.UTC()
		if graceUntil.After(now) && !graceUntil.After(now.Add(window)) {
			events = append(events, Notification{
				EventKey: "grace:" + sub.ID + ":" + strconv.FormatInt(graceUntil.Unix(), 10),
				Kind:     "grace",
				Text:     "Льготный период закончится " + formatTime(graceUntil) + ". Продлите подписку, чтобы не потерять доступ.",
			})
		}
	}
	return events
}

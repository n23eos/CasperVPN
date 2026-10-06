package telegram

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

type memoryNotificationStore struct {
	mu      sync.Mutex
	users   []int64
	sent    map[string]bool
	pending map[string]Notification
}

func (s *memoryNotificationStore) NotificationUsers(context.Context, int) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.users...), nil
}

func notificationStoreKey(telegramID int64, eventKey string) string {
	return eventKey + ":" + time.Unix(telegramID, 0).UTC().Format(time.RFC3339)
}

func (s *memoryNotificationStore) ReconcileNotifications(_ context.Context, telegramID int64, current []Notification, allowActivation bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, notification := range s.pending {
		if strings.HasSuffix(key, time.Unix(telegramID, 0).UTC().Format(time.RFC3339)) &&
			(notification.Kind == "expiry" || notification.Kind == "grace" || (!allowActivation && notification.Kind == "activation")) {
			delete(s.pending, key)
		}
	}
	for _, notification := range current {
		key := notificationStoreKey(telegramID, notification.EventKey)
		if !s.sent[key] {
			s.pending[key] = notification
		}
	}
	return nil
}

func (s *memoryNotificationStore) PendingNotifications(_ context.Context, telegramID int64, limit int) ([]Notification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0)
	suffix := time.Unix(telegramID, 0).UTC().Format(time.RFC3339)
	for key := range s.pending {
		if strings.HasSuffix(key, suffix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	notifications := make([]Notification, 0, len(keys))
	for _, key := range keys {
		notifications = append(notifications, s.pending[key])
	}
	return notifications, nil
}

func (s *memoryNotificationStore) MarkNotificationSent(_ context.Context, telegramID int64, eventKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := notificationStoreKey(telegramID, eventKey)
	if _, ok := s.pending[key]; !ok {
		return errors.New("pending notification not found")
	}
	delete(s.pending, key)
	s.sent[key] = true
	return nil
}

func activeNotificationStatus(now time.Time) AccountStatus {
	expires := now.Add(7 * 24 * time.Hour)
	return AccountStatus{
		UserStatus: contracts.UserStatusActive,
		Invoice:    &LatestInvoice{ID: "inv-1", Status: "settled"},
		Subscription: &contracts.Subscription{
			ID: "sub-1", UserID: "user-1", Plan: contracts.SubscriptionPlanBasic,
			Status: contracts.SubscriptionStatusActive, ExpiresAt: &expires,
		},
		Eligible: true,
	}
}

func newTestNotifier(t *testing.T, api BotAPI, onboarding Onboarding, store NotificationStore, now time.Time) *Notifier {
	t.Helper()
	notifier, err := NewNotifier(api, onboarding, store, NotifierConfig{
		Interval: time.Minute, Window: 72 * time.Hour, BatchSize: 10,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewNotifier: %v", err)
	}
	return notifier
}

func TestNotifierRestartDoesNotRepeatRecordedActivation(t *testing.T) {
	now := time.Now().UTC()
	api := &fakeAPI{}
	onboarding := &fakeOnboarding{status: activeNotificationStatus(now)}
	store := &memoryNotificationStore{users: []int64{42}, sent: map[string]bool{}, pending: map[string]Notification{}}
	if err := newTestNotifier(t, api, onboarding, store, now).RunOnce(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := newTestNotifier(t, api, onboarding, store, now).RunOnce(context.Background()); err != nil {
		t.Fatalf("restart run: %v", err)
	}
	if api.count() != 1 || !strings.Contains(api.last().text, "доступ уже активирован") {
		t.Fatalf("notifications after restart = %d, last=%q", api.count(), api.last().text)
	}
}

func TestNotifierRetriesFailedSendWithoutRecording(t *testing.T) {
	now := time.Now().UTC()
	api := &fakeAPI{failures: 1}
	onboarding := &fakeOnboarding{status: activeNotificationStatus(now)}
	store := &memoryNotificationStore{users: []int64{42}, sent: map[string]bool{}, pending: map[string]Notification{}}
	notifier := newTestNotifier(t, api, onboarding, store, now)
	if err := notifier.RunOnce(context.Background()); err == nil {
		t.Fatal("failed send must be reported")
	}
	if api.count() != 0 || len(store.sent) != 0 || len(store.pending) != 1 {
		t.Fatalf("failed send count=%d markers=%v pending=%v", api.count(), store.sent, store.pending)
	}
	if err := notifier.RunOnce(context.Background()); err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if api.count() != 1 || len(store.sent) != 1 {
		t.Fatalf("retry count=%d markers=%v", api.count(), store.sent)
	}
}

func TestNotificationExpiryAndGraceTimeBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	window := 72 * time.Hour
	tests := []struct {
		name       string
		expiresAt  time.Time
		graceUntil *time.Time
		eligible   bool
		wantKey    string
	}{
		{name: "expiry at window", expiresAt: now.Add(window), eligible: true, wantKey: "expiry:"},
		{name: "outside window", expiresAt: now.Add(window + time.Second), eligible: true},
		{name: "grace after expiry", expiresAt: now.Add(-time.Second), graceUntil: timePointerForNotifier(now.Add(time.Hour)), eligible: true, wantKey: "grace:"},
		{name: "ineligible suppresses reminder", expiresAt: now.Add(time.Hour), eligible: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := AccountStatus{
				UserStatus: contracts.UserStatusActive, Eligible: tt.eligible,
				Subscription: &contracts.Subscription{
					ID: "sub-1", UserID: "user-1", Plan: contracts.SubscriptionPlanBasic,
					Status: contracts.SubscriptionStatusActive, ExpiresAt: &tt.expiresAt, GraceUntil: tt.graceUntil,
				},
			}
			events := notificationEvents(status, now, window)
			if tt.wantKey == "" {
				if len(events) != 0 {
					t.Fatalf("events = %+v", events)
				}
				return
			}
			if len(events) != 1 || !strings.HasPrefix(events[0].EventKey, tt.wantKey) {
				t.Fatalf("events = %+v", events)
			}
		})
	}
}

func TestNotifierContinuesAfterUnavailableUser(t *testing.T) {
	now := time.Now().UTC()
	api := &fakeAPI{}
	store := &memoryNotificationStore{users: []int64{42}, sent: map[string]bool{}, pending: map[string]Notification{}}
	onboarding := &failingStatusOnboarding{fakeOnboarding: fakeOnboarding{}, err: errors.New("unavailable")}
	if err := newTestNotifier(t, api, onboarding, store, now).RunOnce(context.Background()); err == nil {
		t.Fatal("unavailable status must be reported")
	}
	if api.count() != 0 || len(store.sent) != 0 {
		t.Fatal("unavailable upstream must not emit or record a notification")
	}
}

func TestNotifierRetriesDurableActivationAfterLatestInvoiceChanges(t *testing.T) {
	now := time.Now().UTC()
	api := &fakeAPI{failures: 1}
	onboarding := &fakeOnboarding{status: activeNotificationStatus(now)}
	store := &memoryNotificationStore{users: []int64{42}, sent: map[string]bool{}, pending: map[string]Notification{}}
	notifier := newTestNotifier(t, api, onboarding, store, now)
	if err := notifier.RunOnce(context.Background()); err == nil {
		t.Fatal("first send must fail")
	}
	onboarding.mu.Lock()
	onboarding.status.Invoice = &LatestInvoice{ID: "inv-2", Status: "pending"}
	onboarding.mu.Unlock()
	if err := notifier.RunOnce(context.Background()); err != nil {
		t.Fatalf("retry after latest changed: %v", err)
	}
	if api.count() != 1 || !strings.Contains(api.last().text, "доступ уже активирован") {
		t.Fatalf("durable activation retry count=%d text=%q", api.count(), api.last().text)
	}
}

func TestNotifierCancelsStaleExpiryAfterRenewal(t *testing.T) {
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	api := &fakeAPI{failures: 1}
	status := activeNotificationStatus(now)
	status.Invoice = nil
	status.Subscription.ExpiresAt = &expires
	onboarding := &fakeOnboarding{status: status}
	store := &memoryNotificationStore{users: []int64{42}, sent: map[string]bool{}, pending: map[string]Notification{}}
	notifier := newTestNotifier(t, api, onboarding, store, now)
	if err := notifier.RunOnce(context.Background()); err == nil {
		t.Fatal("first expiry send must fail")
	}
	renewed := now.Add(30 * 24 * time.Hour)
	onboarding.mu.Lock()
	onboarding.status.Subscription.ExpiresAt = &renewed
	onboarding.mu.Unlock()
	if err := notifier.RunOnce(context.Background()); err != nil {
		t.Fatalf("reconcile renewal: %v", err)
	}
	if api.count() != 0 || len(store.pending) != 0 {
		t.Fatalf("stale reminder sent=%d pending=%v", api.count(), store.pending)
	}
}

type failingStatusOnboarding struct {
	fakeOnboarding
	err error
}

func (f *failingStatusOnboarding) Status(context.Context, int64) (AccountStatus, error) {
	return AccountStatus{}, f.err
}

func timePointerForNotifier(value time.Time) *time.Time { return &value }

package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

func TestOperatorSnapshotBoundedSortedAndSafe(t *testing.T) {
	users := NewUsers()
	subs := NewSubscriptions().WithUsers(users)
	now := time.Now().UTC()
	contact := "secret-contact"
	telegram := int64(7654321)
	subID := "paid"
	for i := 0; i < 55; i++ {
		u := contracts.User{ID: fmt.Sprintf("account-%02d", i), Status: contracts.UserStatusActive, UpdatedAt: now,
			TelegramID: &telegram, Email: &contact, PrivateKey: "secret-key", UUID: "secret-uuid", Hysteria2Password: "secret-password"}
		if i == 54 {
			u.SubscriptionID = &subID
		}
		// Direct seed permits arbitrary sensitive values; the reader must project them away.
		users.users[u.ID] = u
	}
	until := now.Add(time.Hour)
	expectedUntil := until
	subs.subs[subID] = contracts.Subscription{ID: subID, UserID: "account-54", Plan: contracts.SubscriptionPlanBasic, Status: contracts.SubscriptionStatusActive, ExpiresAt: &until, Token: "secret-token"}
	got, err := NewOperatorReader(users, subs).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Users[contracts.UserStatusActive] != 55 || got.Subscriptions[contracts.SubscriptionStatusActive] != 1 || len(got.Recent) != 50 {
		t.Fatalf("wrong overview totals: %+v", got)
	}
	if got.Recent[0].ID != "account-54" || got.Recent[49].ID != "account-05" || got.Recent[0].Plan != contracts.SubscriptionPlanBasic {
		t.Fatal("unstable order or missing entitlement")
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{contact, "secret-key", "secret-uuid", "secret-password", "secret-token", "telegram_id"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	// Returned pointer values must not alias mutable stored entitlement values.
	*got.Recent[0].ExpiresAt = now
	if !subs.subs[subID].ExpiresAt.Equal(expectedUntil) {
		t.Fatal("overview aliases stored expiry")
	}
}

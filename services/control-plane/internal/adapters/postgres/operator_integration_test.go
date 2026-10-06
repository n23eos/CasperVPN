//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/postgres"
)

func TestIntegration_OperatorOverviewCountsOrderingBoundAndNoSecrets(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := postgres.NewUserStore(pool)
	subs := postgres.NewSubscriptionStore(pool)
	now := time.Now().UTC()
	contact := "hidden-contact"
	telegram := int64(778899)
	for i := 0; i < 55; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		user := contracts.User{ID: id, Status: contracts.UserStatusActive, UUID: fmt.Sprintf("11111111-1111-4111-8111-%012d", i), RealityShortID: fmt.Sprintf("hidden-sid-%02d", i), PrivateKey: "hidden-private", Hysteria2Password: "hidden-hy2", CreatedAt: now, UpdatedAt: now}
		if i == 54 {
			user.TelegramID = &telegram
			user.Email = &contact
		}
		if err := users.Create(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	until := now.Add(time.Hour)
	sub := contracts.Subscription{ID: "22222222-2222-4222-8222-222222222222", UserID: "00000000-0000-4000-8000-000000000054", Plan: contracts.SubscriptionPlanBasic, Status: contracts.SubscriptionStatusActive, StartsAt: now, ExpiresAt: &until, CreatedAt: now, UpdatedAt: now}
	if err := subs.CreateAndLink(ctx, sub, "hidden-tokenhash", "hidden-prefix", sub.UserID); err != nil {
		t.Fatal(err)
	}
	got, err := postgres.NewOperatorReader(pool).Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Users[contracts.UserStatusActive] != 55 || got.Subscriptions[contracts.SubscriptionStatusActive] != 1 || len(got.Recent) != 50 {
		t.Fatalf("wrong overview: %+v", got)
	}
	if got.Recent[0].ID != "00000000-0000-4000-8000-000000000054" || got.Recent[49].ID != "00000000-0000-4000-8000-000000000005" || got.Recent[0].Plan != contracts.SubscriptionPlanBasic {
		t.Fatal("sort or join mismatch")
	}
	raw, _ := json.Marshal(got)
	for _, value := range []string{contact, "11111111-1111-4111-8111", "hidden-sid", "hidden-private", "hidden-hy2", "hidden-tokenhash", "telegram_id"} {
		if strings.Contains(string(raw), value) {
			t.Fatalf("leaked %s", value)
		}
	}
}

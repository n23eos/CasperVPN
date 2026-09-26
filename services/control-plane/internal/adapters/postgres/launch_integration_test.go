//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/postgres"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/secret"
	"github.com/caspervpn/control-plane/internal/usecase"
)

func TestIntegration_LaunchCreditFenceTokensAndRestart(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	users := postgres.NewUserStore(pool)
	subs := postgres.NewSubscriptionStore(pool)
	rot := postgres.NewRotationStore(pool)
	uSvc := usecase.NewUserService(users, rot, nil)
	cipher, err := secret.NewTokenCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc := usecase.NewSubscriptionService(subs, users).WithTokenCipher(cipher)
	u, err := uSvc.EnsureTelegram(ctx, 9191)
	if err != nil {
		t.Fatal(err)
	}
	again, err := uSvc.EnsureTelegram(ctx, 9191)
	if err != nil || again.ID != u.ID {
		t.Fatal("identity replay", err)
	}
	s, err := svc.Ensure(ctx, u.ID, contracts.SubscriptionPlanBasic)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != contracts.SubscriptionStatusExpired || s.Token != "" {
		t.Fatal("unpaid subscription grants access")
	}
	if _, err = svc.DeliveryLink(ctx, s.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unpaid alias issued", err)
	}
	expiry := time.Now().UTC().Add(time.Hour)
	state := contracts.BillingState{Revision: 2, Status: contracts.SubscriptionStatusActive, ExpiresAt: expiry, GraceUntil: expiry.Add(time.Hour)}
	applied, err := svc.ApplyBillingState(ctx, s.ID, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyBillingState(ctx, s.ID, state); err != nil {
		t.Fatal("nanosecond timestamp replay", err)
	}
	stale := state
	stale.Revision = 1
	stale.Status = contracts.SubscriptionStatusExpired
	got, err := svc.ApplyBillingState(ctx, s.ID, stale)
	if err != nil || got.BillingRevision != 2 || !got.ExpiresAt.Equal(*applied.ExpiresAt) {
		t.Fatal("stale revision overwrote period", err)
	}
	conflict := state
	conflict.GraceUntil = conflict.GraceUntil.Add(time.Hour)
	if _, err = svc.ApplyBillingState(ctx, s.ID, conflict); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("mismatching equal revision accepted", err)
	}
	if err = subs.Update(ctx, contracts.Subscription{ID: s.ID, Status: contracts.SubscriptionStatusExpired}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("legacy SQL writer bypassed revision", err)
	}
	upgrade := state
	upgrade.Revision = 3
	upgrade.Plan = contracts.SubscriptionPlanUnlimited
	upgraded, err := svc.ApplyBillingState(ctx, s.ID, upgrade)
	if err != nil || upgraded.Plan != contracts.SubscriptionPlanUnlimited || upgraded.DeviceLimit != 5 || upgraded.TrafficLimitBytes != 0 {
		t.Fatal("upgrade limits", err)
	}
	if _, err = svc.ApplyBillingState(ctx, s.ID, upgrade); err != nil {
		t.Fatal("upgrade replay", err)
	}
	conflictingPlan := upgrade
	conflictingPlan.Plan = contracts.SubscriptionPlanBasic
	if _, err = svc.ApplyBillingState(ctx, s.ID, conflictingPlan); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("same revision changes plan", err)
	}
	link, err := svc.DeliveryLink(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Fresh service and repository instances model restart: only DB and external key survive.
	restarted := usecase.NewSubscriptionService(postgres.NewSubscriptionStore(pool), postgres.NewUserStore(pool)).WithTokenCipher(cipher)
	link2, err := restarted.DeliveryLink(ctx, s.ID)
	if err != nil || link2.Token != link.Token {
		t.Fatal("delivery token changed after restart", err)
	}
	var ciphertext []byte
	var hash string
	if err = pool.QueryRow(ctx, `SELECT ciphertext,token_hash FROM subscription_delivery_tokens WHERE subscription_id=$1`, s.ID).Scan(&ciphertext, &hash); err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == link.Token || hash == link.Token || hash != secret.HashToken(link.Token) {
		t.Fatal("token persisted without protection")
	}
	if _, err = restarted.ResolveToken(ctx, hash); err != nil {
		t.Fatal("alias resolution", err)
	}
	rotated, err := svc.RotateToken(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ResolveToken(ctx, hash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("rotation did not revoke delivery alias", err)
	}
	if _, err = svc.ResolveToken(ctx, secret.HashToken(rotated.PlainToken)); err != nil {
		t.Fatal("new primary hash not authoritative", err)
	}
	if _, err = svc.Cancel(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.ApplyBillingState(ctx, s.ID, upgrade)
	if err != nil || replay.Status != contracts.SubscriptionStatusCanceled {
		t.Fatal("credit replay after cancellation", err)
	}
}
func TestIntegration_ConcurrentIdentitySubscriptionAndLink(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	users := postgres.NewUserStore(pool)
	subs := postgres.NewSubscriptionStore(pool)
	rot := postgres.NewRotationStore(pool)
	userSvc := usecase.NewUserService(users, rot, nil)
	cipher, _ := secret.NewTokenCipher(make([]byte, 32))
	svc := usecase.NewSubscriptionService(subs, users).WithTokenCipher(cipher)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := userSvc.EnsureTelegram(ctx, 8172)
			if err != nil {
				errs <- err
				return
			}
			s, err := svc.Ensure(ctx, u.ID, contracts.SubscriptionPlanBasic)
			if err != nil {
				errs <- err
				return
			}
			ids <- s.ID
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Fatal(err)
	}
	id := ""
	for got := range ids {
		if id != "" && got != id {
			t.Fatal("duplicate subscription")
		}
		id = got
	}
	expires := time.Now().Add(time.Hour)
	if _, err := svc.ApplyBillingState(ctx, id, contracts.BillingState{Revision: 1, Status: contracts.SubscriptionStatusActive, ExpiresAt: expires, GraceUntil: expires}); err != nil {
		t.Fatal(err)
	}
	tokens := make(chan string, 8)
	errs = make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := svc.DeliveryLink(ctx, id)
			if err != nil {
				errs <- err
				return
			}
			tokens <- l.Token
		}()
	}
	wg.Wait()
	close(tokens)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	token := ""
	for got := range tokens {
		if token != "" && token != got {
			t.Fatal("multiple delivery aliases")
		}
		token = got
	}
}
func TestIntegration_AccessGraceIsolationAndRevocation(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	users := postgres.NewUserStore(pool)
	subs := postgres.NewSubscriptionStore(pool)
	rot := postgres.NewRotationStore(pool)
	userSvc := usecase.NewUserService(users, rot, nil)
	svc := usecase.NewSubscriptionService(subs, users)
	u1, err := userSvc.Create(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	u2, err := userSvc.Create(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if u1.Hysteria2Password == "" || u1.Hysteria2Password == u2.Hysteria2Password {
		t.Fatal("credentials not isolated")
	}
	s1, err := svc.Ensure(ctx, u1.ID, contracts.SubscriptionPlanBasic)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := svc.Ensure(ctx, u2.ID, contracts.SubscriptionPlanBasic)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	state := contracts.BillingState{Revision: 1, Status: contracts.SubscriptionStatusPastDue, ExpiresAt: now.Add(-time.Minute), GraceUntil: now.Add(time.Minute)}
	for _, id := range []string{s1.ID, s2.ID} {
		if _, err = svc.ApplyBillingState(ctx, id, state); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := users.EligibleAccessUsers(ctx)
	if err != nil || len(snap.Users) != 2 {
		t.Fatal("grace admission disagrees", len(snap.Users), err)
	}
	if snap.ValidUntil.After(state.GraceUntil) {
		t.Fatal("node lease outlives grace")
	}
	// Time based expiry occurs without status mutation.
	if _, err = pool.Exec(ctx, `UPDATE subscriptions SET expires_at=now()-interval '2 minutes',grace_until=now()-interval '1 minute' WHERE id=$1`, s1.ID); err != nil {
		t.Fatal(err)
	}
	next, err := users.EligibleAccessUsers(ctx)
	if err != nil || len(next.Users) != 1 || next.Users[0].UUID != u2.UUID || next.Revision == snap.Revision {
		t.Fatal("expiry failed to remove exactly one user", err)
	}
	before := next.Users[0].Hysteria2Password
	rotated, err := users.RotateSecrets(ctx, u2.ID, "aabbccdd11223344", u2.UUID, "new-private-material")
	if err != nil {
		t.Fatal(err)
	}
	next, err = users.EligibleAccessUsers(ctx)
	if err != nil || next.Users[0].Hysteria2Password == before || next.Users[0].Hysteria2Password != rotated.Hysteria2Password {
		t.Fatal("Hy2 rotation not atomic", err)
	}
}

package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/secret"
)

// makeUserAndSub seeds a user + subscription and returns the services wired to
// the given revoker.
func makeUserAndSub(t *testing.T, rev domain.SubscriptionRevoker) (*UserService, *SubscriptionService, *fakeSubRepo, contracts.User, domain.SubscriptionWithToken) {
	t.Helper()
	users := newFakeUserRepo()
	subs := newFakeSubRepo()
	usvc := NewUserService(users, newFakeRotationRepo(), newFakeQueue()).WithRevoker(rev)
	ssvc := NewSubscriptionService(subs, users).WithRevoker(rev)
	u, err := usvc.Create(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	res, err := ssvc.Create(context.Background(), u.ID, contracts.SubscriptionPlanBasic)
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	return usvc, ssvc, subs, u, res
}

func TestSubscriptionCreate_RegistersTokenDownstream(t *testing.T) {
	rev := newFakeRevoker()
	_, _, _, u, res := makeUserAndSub(t, rev)
	if len(rev.registered) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(rev.registered))
	}
	r := rev.registered[0]
	if r.token != res.PlainToken || r.userID != u.ID || r.subID != res.Subscription.ID {
		t.Fatalf("registration mismatch: %+v", r)
	}
}

func TestSubscriptionPatch_TruePartialUpdate(t *testing.T) {
	ctx := context.Background()
	rev := newFakeRevoker()
	_, ssvc, subs, _, res := makeUserAndSub(t, rev)
	id := res.Subscription.ID

	// Patch only expires_at: status must be untouched.
	exp := time.Now().Add(30 * 24 * time.Hour).UTC()
	got, err := ssvc.Patch(ctx, id, contracts.SubscriptionPatch{ExpiresAt: &exp})
	if err != nil {
		t.Fatalf("patch expires_at: %v", err)
	}
	if got.Status != contracts.SubscriptionStatusActive {
		t.Fatalf("status changed by expires_at-only patch: %s", got.Status)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Fatalf("expires_at not applied: %+v", got.ExpiresAt)
	}
	if len(rev.revokedSubs) != 0 {
		t.Fatalf("servable patch must not revoke, got %v", rev.revokedSubs)
	}

	// Patch only status -> expired: expires_at must survive, token revoked.
	expired := contracts.SubscriptionStatusExpired
	got, err = ssvc.Patch(ctx, id, contracts.SubscriptionPatch{Status: &expired})
	if err != nil {
		t.Fatalf("patch status: %v", err)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Fatalf("expires_at lost on status patch: %+v", got.ExpiresAt)
	}
	if len(rev.revokedSubs) != 1 || rev.revokedSubs[0] != id {
		t.Fatalf("non-servable patch must revoke downstream, got %v", rev.revokedSubs)
	}
	if got.Token != "" || subs.subs[id].Token != "" {
		t.Fatal("patch must never expose or persist the token")
	}
}

func TestSubscriptionPatch_Validation(t *testing.T) {
	ctx := context.Background()
	_, ssvc, _, _, res := makeUserAndSub(t, newFakeRevoker())

	bogus := contracts.SubscriptionStatus("bogus")
	if _, err := ssvc.Patch(ctx, res.Subscription.ID, contracts.SubscriptionPatch{Status: &bogus}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("want ErrValidation, got %v", err)
	}
	if _, err := ssvc.Patch(ctx, "ghost", contracts.SubscriptionPatch{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSubscriptionCancel_SetsStatusAndRevokes(t *testing.T) {
	ctx := context.Background()
	rev := newFakeRevoker()
	_, ssvc, _, _, res := makeUserAndSub(t, rev)

	got, err := ssvc.Cancel(ctx, res.Subscription.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got.Status != contracts.SubscriptionStatusCanceled {
		t.Fatalf("status = %s, want canceled", got.Status)
	}
	if len(rev.revokedSubs) != 1 || rev.revokedSubs[0] != res.Subscription.ID {
		t.Fatalf("cancel must revoke downstream, got %v", rev.revokedSubs)
	}
}

func TestSubscriptionRotateToken_SwapsHashRevokesAndReregisters(t *testing.T) {
	ctx := context.Background()
	rev := newFakeRevoker()
	_, ssvc, subs, u, res := makeUserAndSub(t, rev)
	id := res.Subscription.ID
	oldHash := subs.hashes[id]

	rot, err := ssvc.RotateToken(ctx, id)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rot.PlainToken == "" || rot.PlainToken == res.PlainToken {
		t.Fatal("expected a fresh plaintext token")
	}
	if subs.hashes[id] == oldHash {
		t.Fatal("stored hash must change on rotation")
	}
	if subs.hashes[id] != secret.HashToken(rot.PlainToken) {
		t.Fatal("stored hash does not match new token")
	}
	if subs.subs[id].Token != "" {
		t.Fatal("plaintext token must not be persisted")
	}
	if len(rev.revokedSubs) != 1 || rev.revokedSubs[0] != id {
		t.Fatalf("rotation must revoke the old link, got %v", rev.revokedSubs)
	}
	// Create + rotate = 2 registrations; the last one carries the new token.
	if len(rev.registered) != 2 {
		t.Fatalf("expected 2 registrations, got %d", len(rev.registered))
	}
	last := rev.registered[len(rev.registered)-1]
	if last.token != rot.PlainToken || last.userID != u.ID || last.subID != id {
		t.Fatalf("re-registration mismatch: %+v", last)
	}
}

func TestUserBan_RevokesAllTokensDownstream(t *testing.T) {
	ctx := context.Background()
	rev := newFakeRevoker()
	usvc, _, _, u, _ := makeUserAndSub(t, rev)

	patch := u
	patch.Status = contracts.UserStatusBanned
	if _, err := usvc.Update(ctx, u.ID, patch, "admin"); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if len(rev.revokedUsers) != 1 || rev.revokedUsers[0] != u.ID {
		t.Fatalf("ban must revoke the user's tokens downstream, got %v", rev.revokedUsers)
	}

	// A no-op update of an active user must not revoke.
	rev2 := newFakeRevoker()
	usvc2, _, _, u2, _ := makeUserAndSub(t, rev2)
	if _, err := usvc2.Update(ctx, u2.ID, u2, "admin"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(rev2.revokedUsers) != 0 {
		t.Fatalf("active-user update must not revoke, got %v", rev2.revokedUsers)
	}
}

func TestRotateSecrets_RevokesDownstream(t *testing.T) {
	ctx := context.Background()
	rev := newFakeRevoker()
	usvc, _, _, u, _ := makeUserAndSub(t, rev)

	if _, err := usvc.RotateSecrets(ctx, u.ID, "abuse", "admin"); err != nil {
		t.Fatalf("rotate secrets: %v", err)
	}
	if len(rev.revokedUsers) != 1 || rev.revokedUsers[0] != u.ID {
		t.Fatalf("secret rotation must revoke downstream, got %v", rev.revokedUsers)
	}
}

func TestRevokerFailure_IsBestEffort(t *testing.T) {
	ctx := context.Background()
	rev := newFakeRevoker()
	rev.fail = true
	usvc, ssvc, _, u, res := makeUserAndSub(t, rev)

	// Local state changes must succeed even when the notifier is down; the
	// subscription service converges via cache TTL + status re-check on miss.
	patch := u
	patch.Status = contracts.UserStatusBanned
	if _, err := usvc.Update(ctx, u.ID, patch, "admin"); err != nil {
		t.Fatalf("ban with failing revoker: %v", err)
	}
	if _, err := ssvc.Cancel(ctx, res.Subscription.ID); err != nil {
		t.Fatalf("cancel with failing revoker: %v", err)
	}
	if _, err := ssvc.RotateToken(ctx, res.Subscription.ID); err != nil {
		t.Fatalf("rotate with failing revoker: %v", err)
	}
}

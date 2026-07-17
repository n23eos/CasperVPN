package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
)

// CreateAndLink must be atomic per user: many concurrent creators for the same user
// yield exactly one subscription, and the user ends linked to it. (Process-local
// mutex — memory is not cross-instance safe; that is the Postgres store's job.)
func TestSubscriptions_CreateAndLink_ConcurrentOneWins(t *testing.T) {
	users := NewUsers()
	subs := NewSubscriptions().WithUsers(users)
	ctx := context.Background()
	if err := users.Create(ctx, contracts.User{ID: "u1", Status: contracts.UserStatusActive}); err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	var ok, conflict int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub := contracts.Subscription{ID: fmt.Sprintf("s%d", i), UserID: "u1", Plan: contracts.SubscriptionPlanBasic, Status: contracts.SubscriptionStatusActive}
			switch err := subs.CreateAndLink(ctx, sub, "hash", "pref", "u1"); {
			case err == nil:
				atomic.AddInt32(&ok, 1)
			case errors.Is(err, domain.ErrConflict):
				atomic.AddInt32(&conflict, 1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if ok != 1 || conflict != n-1 {
		t.Fatalf("ok=%d conflict=%d, want 1 and %d", ok, conflict, n-1)
	}
	u, _ := users.Get(ctx, "u1")
	if u.SubscriptionID == nil || *u.SubscriptionID == "" {
		t.Fatal("user not linked to the one subscription")
	}
	subs.mu.Lock()
	total := len(subs.subs)
	subs.mu.Unlock()
	if total != 1 {
		t.Fatalf("stored subscriptions = %d, want 1", total)
	}
}

func TestSubscriptions_CreateAndLink_NotFoundThenConflict(t *testing.T) {
	users := NewUsers()
	subs := NewSubscriptions().WithUsers(users)
	ctx := context.Background()

	if err := subs.CreateAndLink(ctx, contracts.Subscription{ID: "s"}, "h", "p", "ghost"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing user: err = %v, want ErrNotFound", err)
	}
	if err := users.Create(ctx, contracts.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := subs.CreateAndLink(ctx, contracts.Subscription{ID: "s1", UserID: "u1"}, "h", "p", "u1"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := subs.CreateAndLink(ctx, contracts.Subscription{ID: "s2", UserID: "u1"}, "h", "p", "u1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second create: err = %v, want ErrConflict", err)
	}
}

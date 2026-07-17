//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/postgres"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/usecase"
)

// uuidN returns a distinct, valid UUID string (subscriptions.id is a uuid column).
func uuidN(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", i) }

// subFixture builds a valid subscription row for CreateAndLink.
func subFixture(id, userID string) contracts.Subscription {
	now := time.Now().UTC()
	exp := now.Add(720 * time.Hour)
	return contracts.Subscription{
		ID: id, UserID: userID, Plan: contracts.SubscriptionPlanBasic,
		Status:   contracts.SubscriptionStatusActive,
		StartsAt: now, ExpiresAt: &exp, CreatedAt: now, UpdatedAt: now,
	}
}

// Concurrent CreateAndLink for one user: exactly one wins, the rest ErrConflict,
// and the DB ends with a single subscription linked to the user (FOR UPDATE).
func TestIntegration_CreateAndLink_ConcurrentOneWins(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	users := postgres.NewUserStore(pool)
	subs := postgres.NewSubscriptionStore(pool)

	usvc := usecase.NewUserService(users, postgres.NewRotationStore(pool), noQueue{})
	u, err := usvc.Create(ctx, nil, nil)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	var ok, conflict int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch err := subs.CreateAndLink(ctx, subFixture(uuidN(i), u.ID), "hash", "pref", u.ID); {
			case err == nil:
				atomic.AddInt32(&ok, 1)
			case errors.Is(err, domain.ErrConflict):
				atomic.AddInt32(&conflict, 1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if ok != 1 || conflict != n-1 {
		t.Fatalf("ok=%d conflict=%d, want 1 and %d", ok, conflict, n-1)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM subscriptions WHERE user_id=$1`, u.ID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("subscriptions for user = %d, want 1", count)
	}
	var linked *string
	if err := pool.QueryRow(ctx, `SELECT subscription_id FROM users WHERE id=$1`, u.ID).Scan(&linked); err != nil {
		t.Fatalf("read link: %v", err)
	}
	if linked == nil || *linked == "" {
		t.Fatal("user not linked to the one subscription")
	}
}

func TestIntegration_CreateAndLink_ExistingUserConflict(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	users := postgres.NewUserStore(pool)
	subs := postgres.NewSubscriptionStore(pool)
	usvc := usecase.NewUserService(users, postgres.NewRotationStore(pool), noQueue{})
	u, _ := usvc.Create(ctx, nil, nil)

	if err := subs.CreateAndLink(ctx, subFixture(uuidN(1), u.ID), "h", "p", u.ID); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := subs.CreateAndLink(ctx, subFixture(uuidN(2), u.ID), "h", "p", u.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second: err = %v, want ErrConflict", err)
	}
}

// faultQuerier passes everything through except an Exec whose SQL contains failOn.
type faultQuerier struct {
	q      postgres.Querier
	failOn string
}

func (f faultQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, f.failOn) {
		return pgconn.CommandTag{}, errors.New("injected fault")
	}
	return f.q.Exec(ctx, sql, args...)
}
func (f faultQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return f.q.Query(ctx, sql, args...)
}
func (f faultQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return f.q.QueryRow(ctx, sql, args...)
}

// Rollback injection: fail the UPDATE users after the INSERT subscription. The
// caller rolls the tx back, so NEITHER write persists — no orphan subscription and
// the user stays unlinked.
func TestIntegration_CreateAndLink_RollbackInjectionNoOrphan(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	users := postgres.NewUserStore(pool)
	usvc := usecase.NewUserService(users, postgres.NewRotationStore(pool), noQueue{})
	u, _ := usvc.Create(ctx, nil, nil)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	err = postgres.SubscriptionCreateAndLink(ctx, faultQuerier{q: tx, failOn: "UPDATE users"}, subFixture(uuidN(99), u.ID), "h", "p", u.ID)
	if err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("expected the injected fault to surface an error")
	}
	_ = tx.Rollback(ctx) // withTx would do this on the fn error

	var subCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM subscriptions WHERE id=$1`, uuidN(99)).Scan(&subCount); err != nil {
		t.Fatalf("count sub: %v", err)
	}
	if subCount != 0 {
		t.Fatalf("orphan subscription persisted (%d) despite rollback", subCount)
	}
	var linked *string
	if err := pool.QueryRow(ctx, `SELECT subscription_id FROM users WHERE id=$1`, u.ID).Scan(&linked); err != nil {
		t.Fatalf("read link: %v", err)
	}
	if linked != nil && *linked != "" {
		t.Fatalf("user linked (%q) despite rollback", *linked)
	}
}

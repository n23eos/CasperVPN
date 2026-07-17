// Package subscription turns a confirmed payment into an active/renewed
// entitlement. Billing owns the schedule (duration, grace, expiry math); the
// authoritative subscription record lives in control-plane and is updated over the
// Client seam. Renewal always extends from max(now, current expiry) so paying
// early never burns unused days.
package subscription

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caspervpn/billing/internal/controlplane"
	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/plan"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/contracts"
)

// Activator applies plan durations to subscriptions.
type Activator struct {
	cp      controlplane.Client
	catalog *plan.Catalog
	store   store.Repository
	now     func() time.Time
}

// NewActivator wires the activator. now is injectable for deterministic tests.
func NewActivator(cp controlplane.Client, catalog *plan.Catalog, repo store.Repository, now func() time.Time) *Activator {
	if now == nil {
		now = time.Now
	}
	return &Activator{cp: cp, catalog: catalog, store: repo, now: now}
}

// Activate creates or renews the subscription for an anonymous account and returns
// the updated subscription (carrying its sub_token). Safe for both first purchase
// and renewal.
//
// The WHOLE get-user → create-or-reuse → set-period → upsert-schedule sequence runs
// under a per-user lock (store.WithUserLock), so two DISTINCT settlements for the
// same user apply their periods one after another (expiry advances by 2×duration)
// instead of racing and losing one. Cross-instance serialization comes from the
// Postgres advisory lock; the memory backend is single-process only. A concurrent
// winner that slipped past the lock (e.g. two processes on the memory backend) is
// still caught: CreateSubscription returns ErrSubscriptionExists, and this reads the
// user back and reuses the now-linked subscription rather than creating a second.
func (a *Activator) Activate(ctx context.Context, anonUserID string, planID contracts.SubscriptionPlan) (contracts.Subscription, error) {
	p, ok := a.catalog.Get(planID)
	if !ok {
		return contracts.Subscription{}, fmt.Errorf("subscription: unknown plan %q", planID)
	}

	var result contracts.Subscription
	err := a.store.WithUserLock(ctx, anonUserID, func(ctx context.Context) error {
		user, err := a.cp.GetUser(ctx, anonUserID)
		if err != nil {
			return fmt.Errorf("subscription: get user: %w", err)
		}

		var subID string
		if user.SubscriptionID != nil && *user.SubscriptionID != "" {
			subID = *user.SubscriptionID
		} else {
			sub, cerr := a.cp.CreateSubscription(ctx, user.ID, planID)
			switch {
			case errors.Is(cerr, controlplane.ErrSubscriptionExists):
				// A concurrent settlement won the create and linked the subscription;
				// re-read the user and reuse it (never create a second).
				user, err = a.cp.GetUser(ctx, anonUserID)
				if err != nil {
					return fmt.Errorf("subscription: get user after conflict: %w", err)
				}
				if user.SubscriptionID == nil || *user.SubscriptionID == "" {
					return fmt.Errorf("subscription: create conflicted but user still unlinked")
				}
				subID = *user.SubscriptionID
			case cerr != nil:
				return fmt.Errorf("subscription: create: %w", cerr)
			default:
				subID = sub.ID
			}
		}

		// Extend from max(now, current expiry) so paying early never burns unused
		// days — applies to reuse and post-conflict reuse; a fresh subscription has no
		// schedule yet (ErrNotFound), so base stays now.
		base := a.now()
		if sched, serr := a.store.GetSchedule(ctx, subID); serr == nil && sched.ExpiresAt.After(base) {
			base = sched.ExpiresAt
		}
		newExpiry := base.Add(p.Duration)

		sub, err := a.cp.SetSubscriptionPeriod(ctx, subID, contracts.SubscriptionStatusActive, newExpiry)
		if err != nil {
			return fmt.Errorf("subscription: set period: %w", err)
		}
		if err := a.store.UpsertSchedule(ctx, model.Schedule{
			SubID:      subID,
			AnonUserID: user.ID,
			Status:     string(contracts.SubscriptionStatusActive),
			ExpiresAt:  newExpiry,
			GraceUntil: newExpiry.Add(p.Grace),
		}); err != nil {
			return fmt.Errorf("subscription: upsert schedule: %w", err)
		}
		result = sub
		return nil
	})
	return result, err
}

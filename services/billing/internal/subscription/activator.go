// Package subscription turns a confirmed payment into an active/renewed
// entitlement. Billing owns the schedule (duration, grace, expiry math); the
// authoritative subscription record lives in control-plane and is updated over the
// Client seam. Renewal always extends from max(now, current expiry) so paying
// early never burns unused days.
package subscription

import (
	"context"
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
// the updated subscription (carrying its sub_token). It is safe to call for both
// first purchase and renewal; the caller (payment processor) guarantees it runs at
// most once per settled payment, which is what keeps a double webhook from adding a
// double period.
func (a *Activator) Activate(ctx context.Context, anonUserID string, planID contracts.SubscriptionPlan) (contracts.Subscription, error) {
	p, ok := a.catalog.Get(planID)
	if !ok {
		return contracts.Subscription{}, fmt.Errorf("subscription: unknown plan %q", planID)
	}
	user, err := a.cp.GetUser(ctx, anonUserID)
	if err != nil {
		return contracts.Subscription{}, fmt.Errorf("subscription: get user: %w", err)
	}

	base := a.now()
	var subID string
	if user.SubscriptionID != nil && *user.SubscriptionID != "" {
		subID = *user.SubscriptionID
		// Extend from the later of now and the current expiry (don't lose paid days).
		if sched, err := a.store.GetSchedule(ctx, subID); err == nil && sched.ExpiresAt.After(base) {
			base = sched.ExpiresAt
		}
	} else {
		sub, err := a.cp.CreateSubscription(ctx, user.ID, planID)
		if err != nil {
			return contracts.Subscription{}, fmt.Errorf("subscription: create: %w", err)
		}
		subID = sub.ID
	}

	newExpiry := base.Add(p.Duration)
	sub, err := a.cp.SetSubscriptionPeriod(ctx, subID, contracts.SubscriptionStatusActive, newExpiry)
	if err != nil {
		return contracts.Subscription{}, fmt.Errorf("subscription: set period: %w", err)
	}

	if err := a.store.UpsertSchedule(ctx, model.Schedule{
		SubID:      subID,
		AnonUserID: user.ID,
		Status:     string(contracts.SubscriptionStatusActive),
		ExpiresAt:  newExpiry,
		GraceUntil: newExpiry.Add(p.Grace),
	}); err != nil {
		return contracts.Subscription{}, fmt.Errorf("subscription: upsert schedule: %w", err)
	}
	return sub, nil
}

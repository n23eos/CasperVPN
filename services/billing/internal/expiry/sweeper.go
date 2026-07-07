// Package expiry runs the grace/auto-expiry state machine. Billing owns the
// schedule; on each transition it drives the authoritative subscription status in
// control-plane. Flow: active --(past ExpiresAt)--> past_due --(past grace)--> expired.
package expiry

import (
	"context"
	"time"

	"github.com/caspervpn/billing/internal/controlplane"
	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/contracts"
)

// Sweeper advances subscriptions through the grace/expiry lifecycle.
type Sweeper struct {
	store store.Repository
	cp    controlplane.Client
	now   func() time.Time
}

// NewSweeper wires the sweeper. now is injectable for deterministic tests.
func NewSweeper(repo store.Repository, cp controlplane.Client, now func() time.Time) *Sweeper {
	if now == nil {
		now = time.Now
	}
	return &Sweeper{store: repo, cp: cp, now: now}
}

// RunOnce processes every due schedule once. It is idempotent: a schedule already
// in the target status is skipped, so repeated runs are harmless.
func (s *Sweeper) RunOnce(ctx context.Context) error {
	now := s.now()
	due, err := s.store.DueSchedules(ctx, now)
	if err != nil {
		return err
	}
	for _, sched := range due {
		next := transition(sched, now)
		if next == "" || next == sched.Status {
			continue
		}
		if _, err := s.cp.SetSubscriptionPeriod(ctx, sched.SubID, contracts.SubscriptionStatus(next), sched.ExpiresAt); err != nil {
			continue // transient; retried next run
		}
		sched.Status = next
		_ = s.store.UpsertSchedule(ctx, model.Schedule{
			SubID:      sched.SubID,
			AnonUserID: sched.AnonUserID,
			Status:     next,
			ExpiresAt:  sched.ExpiresAt,
			GraceUntil: sched.GraceUntil,
		})
	}
	return nil
}

// transition returns the status a schedule should move to at now, or "" for no
// change. Past the grace window it is fully expired; past expiry but still within
// grace it is past_due.
func transition(s model.Schedule, now time.Time) string {
	switch {
	case !now.Before(s.GraceUntil):
		return string(contracts.SubscriptionStatusExpired)
	case !now.Before(s.ExpiresAt):
		return string(contracts.SubscriptionStatusPastDue)
	default:
		return ""
	}
}

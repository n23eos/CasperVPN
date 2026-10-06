package memory

import (
	"context"
	"sort"

	"github.com/caspervpn/contracts"
)

type OperatorReader struct {
	users         *Users
	subscriptions *Subscriptions
}

func NewOperatorReader(users *Users, subscriptions *Subscriptions) *OperatorReader {
	return &OperatorReader{users: users, subscriptions: subscriptions}
}

func (r *OperatorReader) Read(ctx context.Context) (contracts.ControlPlaneOperatorSummary, error) {
	result := contracts.ControlPlaneOperatorSummary{
		Users:         map[contracts.UserStatus]int64{contracts.UserStatusActive: 0, contracts.UserStatusSuspended: 0, contracts.UserStatusExpired: 0, contracts.UserStatusBanned: 0},
		Subscriptions: map[contracts.SubscriptionStatus]int64{contracts.SubscriptionStatusTrialing: 0, contracts.SubscriptionStatusActive: 0, contracts.SubscriptionStatusPastDue: 0, contracts.SubscriptionStatusCanceled: 0, contracts.SubscriptionStatusExpired: 0},
		Recent:        []contracts.OperatorAccount{},
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	r.users.mu.Lock()
	defer r.users.mu.Unlock()
	r.subscriptions.mu.Lock()
	defer r.subscriptions.mu.Unlock()
	for _, user := range r.users.users {
		result.Users[user.Status]++
		item := contracts.OperatorAccount{ID: user.ID, Status: user.Status, UpdatedAt: user.UpdatedAt}
		if user.SubscriptionID != nil {
			id := *user.SubscriptionID
			item.SubscriptionID = &id
			if sub, ok := r.subscriptions.subs[id]; ok && sub.UserID == user.ID {
				item.Plan = sub.Plan
				item.SubscriptionStatus = sub.Status
				if sub.ExpiresAt != nil {
					v := *sub.ExpiresAt
					item.ExpiresAt = &v
				}
				if sub.GraceUntil != nil {
					v := *sub.GraceUntil
					item.GraceUntil = &v
				}
			}
		}
		result.Recent = append(result.Recent, item)
	}
	for _, sub := range r.subscriptions.subs {
		result.Subscriptions[sub.Status]++
	}
	sort.Slice(result.Recent, func(i, j int) bool {
		a, b := result.Recent[i], result.Recent[j]
		if a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.ID > b.ID
		}
		return a.UpdatedAt.After(b.UpdatedAt)
	})
	if len(result.Recent) > 50 {
		result.Recent = result.Recent[:50]
	}
	return result, nil
}

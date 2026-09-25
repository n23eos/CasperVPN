package memory

import (
	"context"
	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"time"
)

func (r *Users) GetByTelegram(_ context.Context, id int64) (contracts.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if u.TelegramID != nil && *u.TelegramID == id {
			return u, nil
		}
	}
	return contracts.User{}, domain.ErrNotFound
}
func (r *Subscriptions) ApplyBillingState(_ context.Context, id string, state contracts.BillingState) (contracts.Subscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.subs[id]
	if !ok {
		return s, domain.ErrNotFound
	}
	if state.Revision == s.BillingRevision {
		if state.Status != s.Status || s.ExpiresAt == nil || !state.ExpiresAt.Equal(*s.ExpiresAt) || s.GraceUntil == nil || !state.GraceUntil.Equal(*s.GraceUntil) {
			return s, domain.ErrConflict
		}
	}
	if state.Revision > s.BillingRevision {
		s.BillingRevision = state.Revision
		s.Status = state.Status
		s.ExpiresAt = &state.ExpiresAt
		s.GraceUntil = &state.GraceUntil
		s.UpdatedAt = time.Now()
		r.subs[id] = s
	}
	return s, nil
}
func (r *Subscriptions) EnsureDeliveryToken(_ context.Context, id string, candidate domain.DeliveryToken) (domain.DeliveryToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.subs[id]; !ok {
		return domain.DeliveryToken{}, domain.ErrNotFound
	}
	if d, ok := r.delivery[id]; ok {
		return d, nil
	}
	r.delivery[id] = candidate
	return candidate, nil
}
func (r *Subscriptions) ResolveTokenHash(_ context.Context, hash string) (contracts.SubscriptionTokenBinding, error) {
	if r.users != nil {
		r.users.mu.Lock()
		defer r.users.mu.Unlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, s := range r.subs {
		if r.hashes[id] == hash || r.delivery[id].Hash == hash {
			if r.users != nil {
				u := r.users.users[s.UserID]
				if u.Status != contracts.UserStatusActive || u.SubscriptionID == nil || *u.SubscriptionID != id {
					continue
				}
			}
			return contracts.SubscriptionTokenBinding{UserID: s.UserID, SubscriptionID: id}, nil
		}
	}
	return contracts.SubscriptionTokenBinding{}, domain.ErrNotFound
}
func (a *AllowList) EligibleAccessUsers(_ context.Context) (contracts.NodeAccessUsers, error) {
	a.users.mu.Lock()
	defer a.users.mu.Unlock()
	a.subs.mu.Lock()
	defer a.subs.mu.Unlock()
	now := a.now()
	out := contracts.NodeAccessUsers{Users: []contracts.AccessUser{}, ValidUntil: now.Add(5 * time.Minute)}
	for _, u := range a.users.users {
		if u.Status != contracts.UserStatusActive || u.SubscriptionID == nil {
			continue
		}
		s, ok := a.subs.subs[*u.SubscriptionID]
		if !ok || !servable(s.Status) {
			continue
		}
		end := s.AccessUntil()
		if end != nil && !end.After(now) {
			continue
		}
		if u.Hysteria2Password == "" {
			return out, domain.ErrConflict
		}
		out.Users = append(out.Users, contracts.AccessUser{UUID: u.UUID, ShortID: u.RealityShortID, Hysteria2Password: u.Hysteria2Password})
		if end != nil && end.Before(out.ValidUntil) {
			out.ValidUntil = *end
		}
	}
	out.Revision = contracts.AccessUsersRevision(out.Users)
	return out, nil
}

package resolve

import (
	"context"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/subscription/internal/controlplane"
)

// seed builds a Memory store with one active user + active subscription behind a
// token. The subscription's Token is left EMPTY on purpose: control-plane zeroes
// the plaintext token on GET, and resolve must still succeed (regression for the
// dead `sub.Token != token` check that rejected every real resolve).
func seed(token string) *controlplane.Memory {
	m := controlplane.NewMemory()
	m.PutUser(contracts.User{ID: "u1", Status: contracts.UserStatusActive, UUID: "uuid-1", RealityShortID: "ab12cd34"})
	m.PutSubscription(contracts.Subscription{ID: "s1", Status: contracts.SubscriptionStatusActive, Token: ""})
	_ = m.Register(context.Background(), token, "u1", "s1")
	m.SetNodes([]contracts.Node{{ID: "n1", Status: contracts.NodeStatusActive}})
	return m
}

func TestResolve_SucceedsWhenControlPlaneZeroesToken(t *testing.T) {
	m := seed("tok-123")
	r := New(m, m, func() time.Time { return time.Unix(1_000_000, 0) })

	got, err := r.Resolve(context.Background(), "tok-123")
	if err != nil {
		t.Fatalf("Resolve returned error for a valid token: %v", err)
	}
	if got.Bundle.User.ID != "u1" {
		t.Fatalf("wrong user in bundle: %q", got.Bundle.User.ID)
	}
	if len(got.Bundle.Nodes) != 1 {
		t.Fatalf("expected 1 active node, got %d", len(got.Bundle.Nodes))
	}
}

func TestResolve_UnknownTokenRejected(t *testing.T) {
	m := seed("tok-123")
	r := New(m, m, nil)

	if _, err := r.Resolve(context.Background(), "wrong-token"); err != ErrUnknownToken {
		t.Fatalf("expected ErrUnknownToken for bad token, got %v", err)
	}
}

func TestResolve_BannedUserRejected(t *testing.T) {
	m := seed("tok-123")
	m.PutUser(contracts.User{ID: "u1", Status: contracts.UserStatusSuspended, UUID: "uuid-1"})
	r := New(m, m, nil)

	if _, err := r.Resolve(context.Background(), "tok-123"); err != ErrUnknownToken {
		t.Fatalf("expected ErrUnknownToken for suspended user, got %v", err)
	}
}

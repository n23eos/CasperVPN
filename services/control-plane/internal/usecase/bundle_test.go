package usecase

import (
	"context"
	"testing"

	"github.com/caspervpn/contracts"
)

// The user's server-side PrivateKey must never travel into the subscription set
// (neither the persisted at-rest bundle nor the value handed to Agent C). Its
// canonical home is the users table only.
func TestBundleBuild_StripsPrivateKey(t *testing.T) {
	users := newFakeUserRepo()
	nodes := newFakeNodeRepo()
	sets := newFakeSetRepo()

	_ = users.Create(context.Background(), contracts.User{
		ID: "u1", Status: contracts.UserStatusActive,
		UUID: "uuid-1", RealityShortID: "ab12cd34ef56",
		PrivateKey: "SUPER-SECRET-SERVER-KEY",
	})
	_ = nodes.Create(context.Background(), contracts.Node{ID: "n1", Status: contracts.NodeStatusActive})

	svc := NewBundleService(users, nodes, sets)

	set, err := svc.Build(context.Background(), "u1")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if set.Bundle.User.PrivateKey != "" {
		t.Fatalf("built bundle leaks PrivateKey: %q", set.Bundle.User.PrivateKey)
	}
	// The non-secret per-user isolation material must still be present.
	if set.Bundle.User.RealityShortID == "" || set.Bundle.User.UUID == "" {
		t.Fatal("bundle dropped per-user isolation material (short_id/uuid)")
	}

	// And the persisted (at-rest) copy is clean too.
	stored, err := sets.Get(context.Background(), "u1")
	if err != nil {
		t.Fatalf("Get persisted set: %v", err)
	}
	if stored.Bundle.User.PrivateKey != "" {
		t.Fatalf("persisted bundle leaks PrivateKey: %q", stored.Bundle.User.PrivateKey)
	}
}

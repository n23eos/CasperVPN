// Package seed loads a small, idempotent dev dataset so the stack is usable
// right after `make up`. Users and subscriptions go through the real usecases so
// they take the production write path; the demo NODES are written straight to the
// repository because production registration refuses to create an already-active
// node (active is reached only via the guarded activation path, not Register), and
// the dev fleet must come up serving. Dev only.
package seed

import (
	"context"
	"fmt"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/usecase"
)

// Services is the subset of usecases the seeder drives.
type Services struct {
	Nodes   *usecase.NodeService
	Users   *usecase.UserService
	Subs    *usecase.SubscriptionService
	Bundles *usecase.BundleService
	// NodesRepo seeds the demo fleet directly: Register refuses status:active, so the
	// dev seeder writes active nodes to the store (dev-only, trusted setup).
	NodesRepo domain.NodeRepo
}

// Run seeds a demo fleet + user + subscription if the fleet is empty.
func Run(ctx context.Context, s Services) error {
	existing, _, err := s.Nodes.List(ctx, domain.NodeFilter{Limit: 1})
	if err != nil {
		return fmt.Errorf("seed: probe nodes: %w", err)
	}
	if len(existing) > 0 {
		return nil // already seeded
	}

	for _, n := range demoNodes() {
		if err := n.Validate(); err != nil {
			return fmt.Errorf("seed: invalid demo node %s: %w", n.ID, err)
		}
		if err := s.NodesRepo.Create(ctx, n); err != nil {
			return fmt.Errorf("seed: create node %s: %w", n.ID, err)
		}
	}

	tgID := int64(1000001)
	user, err := s.Users.Create(ctx, &tgID, nil)
	if err != nil {
		return fmt.Errorf("seed: create user: %w", err)
	}
	if _, err := s.Subs.Create(ctx, user.ID, contracts.SubscriptionPlanUnlimited); err != nil {
		return fmt.Errorf("seed: create subscription: %w", err)
	}
	if _, err := s.Bundles.Build(ctx, user.ID); err != nil {
		return fmt.Errorf("seed: build set: %w", err)
	}
	return nil
}

// demoNodes are two active nodes with several transports each (diversity, not
// monoculture). All mimicry domains / IPs are DATA here, never hardcoded in the
// service logic.
func demoNodes() []contracts.Node {
	now := time.Now().UTC()
	mk := func(id, region, ip string) contracts.Node {
		return contracts.Node{
			ID: id, Role: contracts.NodeRoleCombined, Status: contracts.NodeStatusActive,
			Provider: "hetzner", Cloud: "eu-a", Region: region, EntryIP: ip,
			EphemeralEntryIP: true, CapacityUsers: 150, CreatedAt: now,
			Transports: []contracts.Transport{
				{
					Tag: "reality-www", Type: contracts.TransportVlessReality,
					Version: contracts.TransportVersionV1, Port: 443, Enabled: true, Priority: 0,
					VlessReality: &contracts.VlessRealityParams{
						ServerNames: []string{"www.microsoft.com"}, Dest: "www.microsoft.com:443",
						PublicKey: "SEED_PUBKEY_" + id, ShortIDs: []string{"0123abcd"},
						Flow: "xtls-rprx-vision", Fingerprint: "chrome",
					},
				},
				{
					Tag: "hy2-mobile", Type: contracts.TransportHysteria2,
					Version: contracts.TransportVersionV1, Port: 8443, Enabled: true, Priority: 1,
					Hysteria2: &contracts.Hysteria2Params{
						Password: "seed-hy2-" + id, SNI: "cdn.jsdelivr.net",
						Obfs: "salamander", ObfsPassword: "seed-obfs-" + id,
					},
				},
			},
		}
	}
	return []contracts.Node{
		mk("node-seed-eu1", "eu-central", "203.0.113.10"),
		mk("node-seed-eu2", "eu-west", "203.0.113.20"),
	}
}

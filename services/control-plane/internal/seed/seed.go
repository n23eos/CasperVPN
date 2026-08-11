// Package seed loads a small, idempotent dev dataset so the stack is usable
// right after `make up`. Users and subscriptions go through the real usecases so
// they take the production write path; the demo NODES are written straight to the
// repository because production registration refuses to create an already-active
// node (active is reached only via the guarded activation path, not Register), and
// the dev fleet must come up serving. Dev only.
//
// The demo fleet itself (mimicry domains, IPs, transports) is DATA, not code: it
// is read from the JSON file named by SEED_NODES_FILE — the [АНТИ-БЛОК] no-hardcode
// rule applies to the seeder too. The dev fixture ships in config/seed.nodes.json.
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// Run seeds the fleet from nodesFile + a demo user + subscription if the fleet
// is empty. nodesFile is required: the demo nodes carry mimicry domains and IPs,
// which must live in data, never in the binary.
func Run(ctx context.Context, s Services, nodesFile string) error {
	if nodesFile == "" {
		return fmt.Errorf("seed: SEED_NODES_FILE not set (mimicry domains/IPs are data, not code)")
	}
	nodes, err := loadNodes(nodesFile)
	if err != nil {
		return err
	}

	existing, _, err := s.Nodes.List(ctx, domain.NodeFilter{Limit: 1})
	if err != nil {
		return fmt.Errorf("seed: probe nodes: %w", err)
	}
	if len(existing) > 0 {
		return nil // already seeded
	}

	for _, n := range nodes {
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

// loadNodes reads and validates the demo fleet. A zero created_at in the file is
// filled with the current time — fixtures should not embed wall-clock values.
func loadNodes(path string) ([]contracts.Node, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("seed: read nodes file: %w", err)
	}
	var nodes []contracts.Node
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return nil, fmt.Errorf("seed: parse %s: %w", path, err)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("seed: %s contains no nodes", path)
	}
	now := time.Now().UTC()
	for i := range nodes {
		if nodes[i].CreatedAt.IsZero() {
			nodes[i].CreatedAt = now
		}
		if err := nodes[i].Validate(); err != nil {
			return nil, fmt.Errorf("seed: invalid node %s in %s: %w", nodes[i].ID, path, err)
		}
	}
	return nodes, nil
}

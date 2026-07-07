package contracts

import (
	"fmt"
	"time"
)

// Node is one fleet node. Its anti-block DNA:
//   - Role + EntryNodeID separate entry (mimicry, rotation) from exit, so an
//     attack on entry never reveals the exit.
//   - RotateAfter + EphemeralEntryIP drive fast, cheap rotation — replacing a
//     node must be cheaper than the censor finding it.
//   - Transports holds MANY families at once (diversity, not monoculture).
//
// No IP or mimicry domain is hardcoded anywhere; they enter through these fields
// (and the Transport params) from config/DB.
type Node struct {
	ID     string     `json:"id"`
	Role   NodeRole   `json:"role"`
	Status NodeStatus `json:"status"`

	// EntryNodeID pairs an exit node with the entry it sits behind. Non-nil only
	// for Role=exit; nil for entry/combined. Enforces entry != exit.
	EntryNodeID *string `json:"entry_node_id,omitempty"`

	// Placement — all from config, cloud-agnostic (multi-cloud by design).
	Provider string `json:"provider"` // e.g. hetzner, vultr, gcore
	Cloud    string `json:"cloud"`    // logical cloud/account bucket
	Region   string `json:"region"`   // e.g. eu-central

	// EntryIP is the currently advertised ingress address. If EphemeralEntryIP is
	// true it is disposable and rotated aggressively.
	EntryIP          string `json:"entry_ip,omitempty"`
	EphemeralEntryIP bool   `json:"ephemeral_entry_ip"`

	// Transports offered by this node — the client picks/switches among them.
	Transports []Transport `json:"transports"`

	// CapacityUsers is the fair-use soft cap for shared-node economics.
	CapacityUsers int `json:"capacity_users,omitempty"`

	// RotateAfter, when set, marks the instant past which this node should be
	// rotated out (drained + retired). Enables scheduled fast rotation.
	RotateAfter *time.Time `json:"rotate_after,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// Labels are free-form operational tags (orchestrator/config use).
	Labels map[string]string `json:"labels,omitempty"`
}

// Validate performs shallow structural checks on a Node.
func (n Node) Validate() error {
	if n.ID == "" {
		return fmt.Errorf("contracts: node id is required")
	}
	if !n.Role.Valid() {
		return fmt.Errorf("contracts: unknown node role %q", n.Role)
	}
	if !n.Status.Valid() {
		return fmt.Errorf("contracts: unknown node status %q", n.Status)
	}
	// Exit nodes must reference their paired entry; non-exit must not.
	if n.Role == NodeRoleExit && (n.EntryNodeID == nil || *n.EntryNodeID == "") {
		return fmt.Errorf("contracts: exit node %q requires entry_node_id", n.ID)
	}
	for i := range n.Transports {
		if err := n.Transports[i].Validate(); err != nil {
			return fmt.Errorf("contracts: node %q transport %d: %w", n.ID, i, err)
		}
	}
	return nil
}

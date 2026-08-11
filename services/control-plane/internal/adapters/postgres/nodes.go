package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
)

const nodeColumns = `id, role, status, entry_node_id, provider, cloud, region,
	entry_ip, ephemeral_entry_ip, capacity_users, rotate_after, labels, created_at, expires_at`

// NodeStore persists the dynamic node registry.
type NodeStore struct {
	pool *pgxpool.Pool
	q    querier
}

// NewNodeStore builds a NodeStore.
func NewNodeStore(pool *pgxpool.Pool) *NodeStore { return &NodeStore{pool: pool, q: pool} }

// Create persists a node and its transports atomically.
func (s *NodeStore) Create(ctx context.Context, n contracts.Node) error {
	return withTx(ctx, s.pool, func(q querier) error {
		labels, err := json.Marshal(orEmptyLabels(n.Labels))
		if err != nil {
			return err
		}
		_, err = q.Exec(ctx, `
			INSERT INTO nodes (`+nodeColumns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			n.ID, string(n.Role), string(n.Status), n.EntryNodeID, n.Provider, n.Cloud, n.Region,
			n.EntryIP, n.EphemeralEntryIP, n.CapacityUsers, n.RotateAfter, labels, n.CreatedAt, n.ExpiresAt)
		if err != nil {
			if isUniqueViolation(err) {
				return domain.ErrConflict
			}
			return fmt.Errorf("postgres: insert node: %w", err)
		}
		return insertTransports(ctx, q, n.ID, n.Transports)
	})
}

// Get returns a node with its transports.
func (s *NodeStore) Get(ctx context.Context, id string) (contracts.Node, error) {
	row := s.q.QueryRow(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id=$1`, id)
	n, err := scanNode(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.Node{}, domain.ErrNotFound
		}
		return contracts.Node{}, err
	}
	tr, err := loadTransports(ctx, s.q, []string{id})
	if err != nil {
		return contracts.Node{}, err
	}
	n.Transports = tr[id]
	return n, nil
}

// List returns a keyset-paginated page of nodes plus a next cursor.
func (s *NodeStore) List(ctx context.Context, f domain.NodeFilter) ([]contracts.Node, string, error) {
	args := []any{f.Cursor}
	where := "WHERE id > $1"
	if f.Status != "" {
		args = append(args, string(f.Status))
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if f.Role != "" {
		args = append(args, string(f.Role))
		where += fmt.Sprintf(" AND role = $%d", len(args))
	}
	if f.Region != "" {
		args = append(args, f.Region)
		where += fmt.Sprintf(" AND region = $%d", len(args))
	}
	limit := f.Limit
	args = append(args, limit+1) // fetch one extra to detect a next page
	q := fmt.Sprintf(`SELECT %s FROM nodes %s ORDER BY id LIMIT $%d`, nodeColumns, where, len(args))

	rows, err := s.q.Query(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("postgres: list nodes: %w", err)
	}
	nodes, err := scanNodes(rows)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(nodes) > limit {
		next = nodes[limit-1].ID
		nodes = nodes[:limit]
	}
	if err := s.attachTransports(ctx, nodes); err != nil {
		return nil, "", err
	}
	return nodes, next, nil
}

// Update replaces a node's row and transports atomically.
func (s *NodeStore) Update(ctx context.Context, n contracts.Node) error {
	return withTx(ctx, s.pool, func(q querier) error {
		labels, err := json.Marshal(orEmptyLabels(n.Labels))
		if err != nil {
			return err
		}
		ct, err := q.Exec(ctx, `
			UPDATE nodes SET role=$2, status=$3, entry_node_id=$4, provider=$5, cloud=$6,
				region=$7, entry_ip=$8, ephemeral_entry_ip=$9, capacity_users=$10,
				rotate_after=$11, labels=$12, expires_at=$13
			WHERE id=$1`,
			n.ID, string(n.Role), string(n.Status), n.EntryNodeID, n.Provider, n.Cloud,
			n.Region, n.EntryIP, n.EphemeralEntryIP, n.CapacityUsers, n.RotateAfter, labels, n.ExpiresAt)
		if err != nil {
			return fmt.Errorf("postgres: update node: %w", err)
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		if _, err := q.Exec(ctx, `DELETE FROM transports WHERE node_id=$1`, n.ID); err != nil {
			return fmt.Errorf("postgres: clear transports: %w", err)
		}
		return insertTransports(ctx, q, n.ID, n.Transports)
	})
}

// SetStatus transitions a node's status and returns the previous value.
func (s *NodeStore) SetStatus(ctx context.Context, id string, status contracts.NodeStatus) (contracts.NodeStatus, error) {
	var prev contracts.NodeStatus
	err := withTx(ctx, s.pool, func(q querier) error {
		var cur string
		if err := q.QueryRow(ctx, `SELECT status FROM nodes WHERE id=$1 FOR UPDATE`, id).Scan(&cur); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}
		prev = contracts.NodeStatus(cur)
		_, err := q.Exec(ctx, `UPDATE nodes SET status=$2 WHERE id=$1`, id, string(status))
		return err
	})
	return prev, err
}

// Activate atomically promotes a provisioning node to active. It runs in a
// SERIALIZABLE transaction and locks the node row (FOR UPDATE): a concurrent
// activation blocks then sees the changed status and 409s, and — crucially — a
// concurrent ban/rotation that changes the eligibility this transaction read
// forces a serialization failure at commit, which maps to a conflict. So a node
// never activates against a stale allow-list.
//
// Guards are ROLE-AWARE, enabling the reconciler's "exit first, entry last"
// sequence (an entry requires its exit already active, which would be impossible
// if the exit itself demanded two client transports + an active exit):
//   - exit:  provisioning + a paired entry (entry_node_id set). No client
//     transport / revision checks — an exit carries no client-facing transport.
//   - entry: provisioning + >=2 DISTINCT enabled client transport types + its
//     paired exit already active + revision == expectedRevision.
//
// Any failed check rolls back with domain.ErrConflict (node stays provisioning).
func (s *NodeStore) Activate(ctx context.Context, id, expectedRevision string, evidence contracts.NodeActivationEvidence) (contracts.Node, contracts.NodeStatus, error) {
	var activated contracts.Node
	var prev contracts.NodeStatus
	err := withSerializableTx(ctx, s.pool, func(q querier) error {
		n, err := scanNode(q.QueryRow(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err // domain.ErrNotFound on no rows
		}
		prev = n.Status
		if n.Status != contracts.NodeStatusProvisioning {
			return fmt.Errorf("%w: node %s is %s, not provisioning", domain.ErrConflict, id, n.Status)
		}

		if n.Role == contracts.NodeRoleExit {
			if n.EntryNodeID == nil || *n.EntryNodeID == "" {
				return fmt.Errorf("%w: exit %s has no paired entry", domain.ErrConflict, id)
			}
			// Structural presence is not readiness: require the reconciler's
			// authenticated entry->exit probe attestation.
			if evidence != contracts.ExitDataPlaneVerified {
				return fmt.Errorf("%w: exit %s activation requires valid data-plane evidence", domain.ErrConflict, id)
			}
		} else {
			tr, err := loadTransports(ctx, q, []string{id})
			if err != nil {
				return err
			}
			n.Transports = tr[id]
			if contracts.DistinctEnabledTransportTypes(n.Transports) < 2 {
				return fmt.Errorf("%w: node %s has fewer than 2 distinct enabled client transports", domain.ErrConflict, id)
			}

			var exitStatus string
			err = q.QueryRow(ctx, `SELECT status FROM nodes WHERE entry_node_id=$1 AND role='exit' LIMIT 1`, id).Scan(&exitStatus)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: node %s has no paired exit", domain.ErrConflict, id)
			}
			if err != nil {
				return err
			}
			if contracts.NodeStatus(exitStatus) != contracts.NodeStatusActive {
				return fmt.Errorf("%w: paired exit of %s is %s, not active", domain.ErrConflict, id, exitStatus)
			}

			users, err := queryEligibleRealityUsersForUpdate(ctx, q)
			if err != nil {
				return err
			}
			if rev := contracts.RealityUsersRevision(users); rev != expectedRevision {
				return fmt.Errorf("%w: allow-list revision changed (have %s, expected %s)", domain.ErrConflict, rev, expectedRevision)
			}
		}

		if _, err := q.Exec(ctx, `UPDATE nodes SET status='active' WHERE id=$1`, id); err != nil {
			return err
		}
		n.Status = contracts.NodeStatusActive
		activated = n
		return nil
	})
	if isSerializationFailure(err) {
		return contracts.Node{}, prev, fmt.Errorf("%w: activation lost a serialization race (eligibility changed concurrently)", domain.ErrConflict)
	}
	return activated, prev, err
}

// Demote atomically sets status=provisioning, but ONLY from a non-terminal state.
// Under one row lock it reads the current status and rejects draining/retired
// (operator-controlled terminal states) with ErrConflict — a stale reconciler
// task must not pull a decommissioned node back into the lifecycle.
func (s *NodeStore) Demote(ctx context.Context, id string) (contracts.NodeStatus, error) {
	var prev contracts.NodeStatus
	err := withTx(ctx, s.pool, func(q querier) error {
		var cur string
		if err := q.QueryRow(ctx, `SELECT status FROM nodes WHERE id=$1 FOR UPDATE`, id).Scan(&cur); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}
		prev = contracts.NodeStatus(cur)
		switch prev {
		case contracts.NodeStatusDraining, contracts.NodeStatusRetired:
			return fmt.Errorf("%w: node %s is %s — refusing to demote a terminal/operator-controlled state", domain.ErrConflict, id, prev)
		}
		_, err := q.Exec(ctx, `UPDATE nodes SET status='provisioning' WHERE id=$1`, id)
		return err
	})
	return prev, err
}

// SetEntryIP updates the advertised ingress address.
func (s *NodeStore) SetEntryIP(ctx context.Context, id, entryIP string) error {
	ct, err := s.q.Exec(ctx, `UPDATE nodes SET entry_ip=$2 WHERE id=$1`, id, entryIP)
	if err != nil {
		return fmt.Errorf("postgres: set entry ip: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListActive returns all active nodes with their transports (the dynamic set).
func (s *NodeStore) ListActive(ctx context.Context) ([]contracts.Node, error) {
	rows, err := s.q.Query(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE status='active' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list active: %w", err)
	}
	nodes, err := scanNodes(rows)
	if err != nil {
		return nil, err
	}
	if err := s.attachTransports(ctx, nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

func (s *NodeStore) attachTransports(ctx context.Context, nodes []contracts.Node) error {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	tr, err := loadTransports(ctx, s.q, ids)
	if err != nil {
		return err
	}
	for i := range nodes {
		nodes[i].Transports = tr[nodes[i].ID]
	}
	return nil
}

func scanNode(row pgx.Row) (contracts.Node, error) {
	var (
		n          contracts.Node
		role, stat string
		labels     []byte
	)
	if err := row.Scan(&n.ID, &role, &stat, &n.EntryNodeID, &n.Provider, &n.Cloud, &n.Region,
		&n.EntryIP, &n.EphemeralEntryIP, &n.CapacityUsers, &n.RotateAfter, &labels, &n.CreatedAt, &n.ExpiresAt); err != nil {
		return contracts.Node{}, err
	}
	n.Role = contracts.NodeRole(role)
	n.Status = contracts.NodeStatus(stat)
	if len(labels) > 0 {
		if err := json.Unmarshal(labels, &n.Labels); err != nil {
			return contracts.Node{}, fmt.Errorf("postgres: labels: %w", err)
		}
	}
	return n, nil
}

func scanNodes(rows pgx.Rows) ([]contracts.Node, error) {
	defer rows.Close()
	var out []contracts.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan node: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func orEmptyLabels(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

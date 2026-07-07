package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

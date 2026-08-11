package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/secret"
)

// SignalStore persists FieldSignal aggregates and their derived verdicts.
type SignalStore struct {
	q querier
}

// NewSignalStore builds a SignalStore.
func NewSignalStore(pool *pgxpool.Pool) *SignalStore { return &SignalStore{q: pool} }

// AddAggregate stores a FieldSignal aggregate together with the derived verdict.
func (s *SignalStore) AddAggregate(ctx context.Context, a domain.SignalAggregate, blockSignals int, verdict domain.Verdict) error {
	id, err := secret.UUIDv4()
	if err != nil {
		return err
	}
	failures := a.Total - a.BySignal[contracts.SignalOK]
	if failures < 0 {
		failures = 0
	}
	_, err = s.q.Exec(ctx, `
		INSERT INTO node_signal_aggregates
			(id, node_id, region, window_start, window_end, total, failures, block_signals, verdict)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		id, a.NodeID, a.Region, a.WindowStart, a.WindowEnd, a.Total, failures, blockSignals, string(verdict))
	if err != nil {
		return fmt.Errorf("postgres: insert signal aggregate: %w", err)
	}
	return nil
}

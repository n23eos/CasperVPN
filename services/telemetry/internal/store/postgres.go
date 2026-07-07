package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/caspervpn/contracts"
)

// PostgresStore is the production time-series backend. It is written against the
// stdlib database/sql interface only — it takes an already-open *sql.DB, so this
// package pulls in NO external driver (keeping the monorepo dependency-free). To
// run it in prod, blank-import a driver (e.g. github.com/jackc/pgx/v5/stdlib) in
// package main, sql.Open a *sql.DB, and pass it to NewPostgresStore. See schema.sql
// for the DDL and docs/telemetry.md for the wiring note.
//
// Every query is parameterized ($1, $2, …) — no string concatenation, no injection.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore wraps an open pool. Caller owns opening/closing the driver.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// WriteSignals inserts a batch inside a single transaction (multi-row write is
// atomic — CLAUDE.md data-integrity rule: no half-applied batches).
func (p *PostgresStore) WriteSignals(ctx context.Context, sigs []contracts.FieldSignal) error {
	if len(sigs) == 0 {
		return nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("telemetry store: begin: %w", err)
	}
	const q = `INSERT INTO telemetry_signals
		(signal_id, node_id, transport_type, transport_version, region, asn, isp,
		 signal_type, success, rtt_ms, loss_pct, client_version, platform, observed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (signal_id) DO NOTHING` // dedup by client key at the DB layer too
	for i := range sigs {
		s := sigs[i]
		if _, err := tx.ExecContext(ctx, q,
			s.SignalID, s.NodeID, string(s.TransportType), string(s.TransportVersion),
			s.Region, s.ASN, s.ISP, string(s.SignalType), s.Success, s.RTTms, s.LossPct,
			s.ClientVersion, string(s.Platform), s.ObservedAt,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("telemetry store: insert signal: %w", err)
		}
	}
	return tx.Commit()
}

// WriteHealth inserts health events in one transaction.
func (p *PostgresStore) WriteHealth(ctx context.Context, events []contracts.HealthEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("telemetry store: begin: %w", err)
	}
	const q = `INSERT INTO telemetry_health
		(node_id, status, probe_source, blocked_from_regions, latency_ms, detail, observed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`
	for i := range events {
		e := events[i]
		if _, err := tx.ExecContext(ctx, q,
			e.NodeID, string(e.Status), e.ProbeSource, pqTextArray(e.BlockedFromRegions),
			e.LatencyMs, e.Detail, e.ObservedAt,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("telemetry store: insert health: %w", err)
		}
	}
	return tx.Commit()
}

// SignalsSince reads signals observed at/after t.
func (p *PostgresStore) SignalsSince(ctx context.Context, t time.Time) ([]contracts.FieldSignal, error) {
	const q = `SELECT signal_id, node_id, transport_type, transport_version, region, asn, isp,
		signal_type, success, rtt_ms, loss_pct, client_version, platform, observed_at
		FROM telemetry_signals WHERE observed_at >= $1 ORDER BY observed_at ASC`
	rows, err := p.db.QueryContext(ctx, q, t)
	if err != nil {
		return nil, fmt.Errorf("telemetry store: query signals: %w", err)
	}
	defer rows.Close()

	var out []contracts.FieldSignal
	for rows.Next() {
		var s contracts.FieldSignal
		var tt, tv, st, plat string
		if err := rows.Scan(&s.SignalID, &s.NodeID, &tt, &tv, &s.Region, &s.ASN, &s.ISP,
			&st, &s.Success, &s.RTTms, &s.LossPct, &s.ClientVersion, &plat, &s.ObservedAt); err != nil {
			return nil, fmt.Errorf("telemetry store: scan signal: %w", err)
		}
		s.TransportType = contracts.TransportType(tt)
		s.TransportVersion = contracts.TransportVersion(tv)
		s.SignalType = contracts.SignalType(st)
		s.Platform = contracts.Platform(plat)
		out = append(out, s)
	}
	return out, rows.Err()
}

// HealthSince reads health events observed at/after t.
func (p *PostgresStore) HealthSince(ctx context.Context, t time.Time) ([]contracts.HealthEvent, error) {
	const q = `SELECT node_id, status, probe_source, blocked_from_regions, latency_ms, detail, observed_at
		FROM telemetry_health WHERE observed_at >= $1 ORDER BY observed_at ASC`
	rows, err := p.db.QueryContext(ctx, q, t)
	if err != nil {
		return nil, fmt.Errorf("telemetry store: query health: %w", err)
	}
	defer rows.Close()

	var out []contracts.HealthEvent
	for rows.Next() {
		var e contracts.HealthEvent
		var status, regions string
		if err := rows.Scan(&e.NodeID, &status, &e.ProbeSource, &regions, &e.LatencyMs,
			&e.Detail, &e.ObservedAt); err != nil {
			return nil, fmt.Errorf("telemetry store: scan health: %w", err)
		}
		e.Status = contracts.HealthStatus(status)
		e.BlockedFromRegions = parseTextArray(regions)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes points observed before t (retention). Two statements, one tx.
func (p *PostgresStore) Prune(ctx context.Context, before time.Time) (int, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("telemetry store: begin prune: %w", err)
	}
	total := 0
	for _, q := range []string{
		`DELETE FROM telemetry_signals WHERE observed_at < $1`,
		`DELETE FROM telemetry_health WHERE observed_at < $1`,
	} {
		res, err := tx.ExecContext(ctx, q, before)
		if err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("telemetry store: prune: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			total += int(n)
		}
	}
	return total, tx.Commit()
}

// Close closes the underlying pool.
func (p *PostgresStore) Close() error { return p.db.Close() }

// pqTextArray renders a Go slice as a Postgres text[] literal ("{a,b}"). Values are
// coarse region codes (validated upstream) — quoted defensively regardless.
func pqTextArray(vals []string) string {
	if len(vals) == 0 {
		return "{}"
	}
	out := "{"
	for i, v := range vals {
		if i > 0 {
			out += ","
		}
		out += quoteArrayElem(v)
	}
	return out + "}"
}

func quoteArrayElem(v string) string {
	q := `"`
	for _, r := range v {
		if r == '"' || r == '\\' {
			q += `\`
		}
		q += string(r)
	}
	return q + `"`
}

// parseTextArray parses a Postgres text[] literal back into a slice. Minimal parser
// for the "{a,b}" shape we write; empty/NULL → nil.
func parseTextArray(lit string) []string {
	if len(lit) < 2 || lit == "{}" {
		return nil
	}
	inner := lit[1 : len(lit)-1]
	var out []string
	var cur []rune
	inQuote := false
	esc := false
	for _, r := range inner {
		switch {
		case esc:
			cur = append(cur, r)
			esc = false
		case r == '\\':
			esc = true
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			out = append(out, string(cur))
			cur = nil
		default:
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

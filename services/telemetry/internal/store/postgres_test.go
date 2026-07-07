package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
)

// This file drives PostgresStore against a tiny in-process fake database/sql driver
// so the SQL methods (Exec/Query/Scan/text[] round-trip) are exercised offline, with
// zero external dependencies. It is NOT a Postgres emulator — it records executed
// statements and returns scripted rows to prove the store's wiring and scanning.

func init() { sql.Register("fakepg-telemetry", &fakeDriver{}) }

var (
	fakeMu  sync.Mutex
	fakeReg = map[string]*fakeDB{}
)

type fakeDB struct {
	execLog    []fakeExec
	signalRows [][]driver.Value
	healthRows [][]driver.Value
	affected   int64
}

type fakeExec struct {
	query string
	args  []driver.Value
}

func newFakeDB(dsn string) *fakeDB {
	db := &fakeDB{}
	fakeMu.Lock()
	fakeReg[dsn] = db
	fakeMu.Unlock()
	return db
}

type fakeDriver struct{}

func (fakeDriver) Open(dsn string) (driver.Conn, error) {
	fakeMu.Lock()
	db := fakeReg[dsn]
	fakeMu.Unlock()
	return &fakeConn{db: db}, nil
}

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{db: c.db, query: q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return fakeTx{}, nil }

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeStmt struct {
	db    *fakeDB
	query string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 } // -1 = do not check arg count

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.db.execLog = append(s.db.execLog, fakeExec{query: s.query, args: args})
	return driver.RowsAffected(s.db.affected), nil
}

func (s *fakeStmt) Query(_ []driver.Value) (driver.Rows, error) {
	if strings.Contains(s.query, "telemetry_signals") {
		return &fakeRows{cols: signalCols, data: s.db.signalRows}, nil
	}
	return &fakeRows{cols: healthCols, data: s.db.healthRows}, nil
}

type fakeRows struct {
	cols []string
	data [][]driver.Value
	pos  int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.pos])
	r.pos++
	return nil
}

var signalCols = []string{
	"signal_id", "node_id", "transport_type", "transport_version", "region", "asn",
	"isp", "signal_type", "success", "rtt_ms", "loss_pct", "client_version", "platform", "observed_at",
}

var healthCols = []string{
	"node_id", "status", "probe_source", "blocked_from_regions", "latency_ms", "detail", "observed_at",
}

func openFake(t *testing.T) (*PostgresStore, *fakeDB) {
	t.Helper()
	dsn := "fake-" + t.Name()
	fdb := newFakeDB(dsn)
	sqlDB, err := sql.Open("fakepg-telemetry", dsn)
	if err != nil {
		t.Fatal(err)
	}
	return NewPostgresStore(sqlDB), fdb
}

func TestPostgres_WriteSignals(t *testing.T) {
	ps, fdb := openFake(t)
	at := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	sigs := []contracts.FieldSignal{
		{SignalID: "a", NodeID: "n1", TransportType: contracts.TransportVlessReality,
			TransportVersion: contracts.TransportVersionV1, Region: "RU-MOW", ASN: 42,
			SignalType: contracts.SignalReset, ObservedAt: at},
		{SignalID: "b", NodeID: "n1", TransportType: contracts.TransportHysteria2,
			Region: "RU-MOW", SignalType: contracts.SignalOK, Success: true, ObservedAt: at},
	}
	if err := ps.WriteSignals(context.Background(), sigs); err != nil {
		t.Fatal(err)
	}
	if len(fdb.execLog) != 2 {
		t.Fatalf("exec count = %d, want 2", len(fdb.execLog))
	}
	if n := len(fdb.execLog[0].args); n != 14 {
		t.Fatalf("insert arg count = %d, want 14", n)
	}
}

func TestPostgres_SignalsSince_Scan(t *testing.T) {
	ps, fdb := openFake(t)
	at := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	fdb.signalRows = [][]driver.Value{{
		"a", "node-x", "vless-reality", "v1", "RU-MOW", int64(42), "SomeISP",
		"dpi_block", false, int64(120), float64(3.5), "1.2.3", "android", at,
	}}
	got, err := ps.SignalsSince(context.Background(), at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	s := got[0]
	if s.TransportType != contracts.TransportVlessReality || s.SignalType != contracts.SignalDPIBlock {
		t.Fatalf("enum reconstruction wrong: %+v", s)
	}
	if s.ASN != 42 || s.RTTms != 120 || s.LossPct != 3.5 || s.Platform != contracts.PlatformAndroid {
		t.Fatalf("scalar scan wrong: %+v", s)
	}
	if !s.ObservedAt.Equal(at) {
		t.Fatalf("time scan wrong: %v", s.ObservedAt)
	}
}

func TestPostgres_HealthSince_ParsesTextArray(t *testing.T) {
	ps, fdb := openFake(t)
	at := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	fdb.healthRows = [][]driver.Value{{
		"node-y", "blocked", "ru-probe-1", "{RU-MOW,IR-07}", int64(0), "", at,
	}}
	got, err := ps.HealthSince(context.Background(), at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	e := got[0]
	if e.Status != contracts.HealthBlocked {
		t.Fatalf("status scan wrong: %q", e.Status)
	}
	if len(e.BlockedFromRegions) != 2 || e.BlockedFromRegions[0] != "RU-MOW" || e.BlockedFromRegions[1] != "IR-07" {
		t.Fatalf("text[] parse wrong: %v", e.BlockedFromRegions)
	}
}

func TestPostgres_Prune(t *testing.T) {
	ps, fdb := openFake(t)
	fdb.affected = 3 // each of the 2 DELETE statements reports 3 rows
	removed, err := ps.Prune(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 6 {
		t.Fatalf("pruned = %d, want 6 (2 statements × 3)", removed)
	}
}

func TestPostgres_WriteHealth(t *testing.T) {
	ps, fdb := openFake(t)
	at := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	events := []contracts.HealthEvent{{
		NodeID: "n1", Status: contracts.HealthBlocked, ProbeSource: "ru-probe-1",
		BlockedFromRegions: []string{"RU-MOW"}, ObservedAt: at,
	}}
	if err := ps.WriteHealth(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	if len(fdb.execLog) != 1 {
		t.Fatalf("exec count = %d, want 1", len(fdb.execLog))
	}
	// blocked_from_regions arg (index 3) must be the text[] literal.
	if got, ok := fdb.execLog[0].args[3].(string); !ok || got != `{"RU-MOW"}` {
		t.Fatalf("text[] arg = %v, want {\"RU-MOW\"}", fdb.execLog[0].args[3])
	}
}

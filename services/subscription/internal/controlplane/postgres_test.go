package controlplane

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"
)

// This file drives the Postgres TokenIndex against a tiny in-process fake
// database/sql driver so the SQL wiring (hashing, Exec/Query/Scan, affected-row
// counts) is exercised offline with zero external dependencies. It is NOT a
// Postgres emulator: it records executed statements and returns scripted rows.

func init() { sql.Register("fakepg-subscription", &fakeDriver{}) }

var (
	fakeMu  sync.Mutex
	fakeReg = map[string]*fakeDB{}
)

type fakeExec struct {
	query string
	args  []driver.Value
}

type fakeDB struct {
	execLog  []fakeExec
	rows     [][]driver.Value // scripted SELECT result for Lookup
	affected int64            // rows reported by DELETE/UPDATE
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

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.db.execLog = append(s.db.execLog, fakeExec{query: s.query, args: args})
	return &fakeRows{cols: []string{"user_id", "subscription_id"}, data: s.db.rows}, nil
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

func openFake(t *testing.T) (*Postgres, *fakeDB) {
	t.Helper()
	dsn := "fake-" + t.Name()
	fdb := newFakeDB(dsn)
	sqlDB, err := sql.Open("fakepg-subscription", dsn)
	if err != nil {
		t.Fatal(err)
	}
	return NewPostgres(sqlDB), fdb
}

func TestPostgresLookupHashesAndScans(t *testing.T) {
	pg, fdb := openFake(t)
	fdb.rows = [][]driver.Value{{"user-1", "sub-1"}}

	uid, sid, err := pg.Lookup(context.Background(), "plaintext-token")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if uid != "user-1" || sid != "sub-1" {
		t.Fatalf("scan wrong: %q %q", uid, sid)
	}
	if len(fdb.execLog) != 1 {
		t.Fatalf("query count = %d, want 1", len(fdb.execLog))
	}
	args := fdb.execLog[0].args
	if len(args) != 1 {
		t.Fatalf("arg count = %d, want 1", len(args))
	}
	if got := args[0].(string); got != HashToken("plaintext-token") {
		t.Fatalf("lookup must query by token hash, got %q", got)
	}
	if got := args[0].(string); strings.Contains(got, "plaintext-token") {
		t.Fatalf("plaintext token leaked into lookup args: %q", got)
	}
}

func TestPostgresLookupNotFound(t *testing.T) {
	pg, fdb := openFake(t)
	fdb.rows = nil // no rows -> sql.ErrNoRows -> ErrNotFound
	if _, _, err := pg.Lookup(context.Background(), "absent"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPostgresRegisterStoresHashOnly(t *testing.T) {
	pg, fdb := openFake(t)
	const token = "leaky-token"
	if err := pg.Register(context.Background(), token, "user-9", "sub-9"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(fdb.execLog) != 1 {
		t.Fatalf("exec count = %d, want 1", len(fdb.execLog))
	}
	args := fdb.execLog[0].args
	if len(args) != 3 {
		t.Fatalf("arg count = %d, want 3", len(args))
	}
	if got := args[0].(string); got != HashToken(token) {
		t.Fatalf("first arg must be the token hash, got %q", got)
	}
	if got := args[0].(string); strings.Contains(got, token) {
		t.Fatalf("plaintext token leaked into the query args: %q", got)
	}
	if args[1].(string) != "user-9" || args[2].(string) != "sub-9" {
		t.Fatalf("user/sub args wrong: %v", args)
	}
}

func TestPostgresRevokeHashesToken(t *testing.T) {
	pg, fdb := openFake(t)
	const token = "revoke-me"
	if err := pg.Revoke(context.Background(), token); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if len(fdb.execLog) != 1 || fdb.execLog[0].args[0].(string) != HashToken(token) {
		t.Fatalf("revoke must delete by token hash, got %v", fdb.execLog)
	}
}

func TestPostgresRevokeByUserReturnsAffected(t *testing.T) {
	pg, fdb := openFake(t)
	fdb.affected = 3
	n, err := pg.RevokeByUser(context.Background(), "user-x")
	if err != nil {
		t.Fatalf("revoke by user: %v", err)
	}
	if n != 3 {
		t.Fatalf("affected = %d, want 3", n)
	}
	if fdb.execLog[0].args[0].(string) != "user-x" {
		t.Fatalf("must filter by user_id, got %v", fdb.execLog[0].args)
	}
}

func TestPostgresRevokeBySubscriptionReturnsAffected(t *testing.T) {
	pg, fdb := openFake(t)
	fdb.affected = 1
	n, err := pg.RevokeBySubscription(context.Background(), "sub-x")
	if err != nil {
		t.Fatalf("revoke by subscription: %v", err)
	}
	if n != 1 {
		t.Fatalf("affected = %d, want 1", n)
	}
	if fdb.execLog[0].args[0].(string) != "sub-x" {
		t.Fatalf("must filter by subscription_id, got %v", fdb.execLog[0].args)
	}
}

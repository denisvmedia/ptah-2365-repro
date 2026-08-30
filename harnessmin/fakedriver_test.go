package harnessmin_test

// database/sql is in the standard library, and the package this reduces spends
// more than half its allocations inside a code path that goes through it:
// (*Migrator).withMigrationLock is 51.67% of the profile cumulatively, and it
// needs a connection, which is why the SQLite-free subset of that package does
// not exercise it at all.
//
// So the machinery -- the pool, the driver interface, rows, statements,
// cancellation across a query -- runs here through a driver of our own. That is
// the part of the workload the C translation was carrying, expressed with
// nothing outside the standard library.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"maps"
	"sync"
)

func init() { sql.Register("harnessfake", fakeDriver{}) }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{}, nil }

type fakeConn struct {
	mu   sync.Mutex
	caps map[string]string
}

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{q: q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return fakeTx{}, nil }

// QueryContext lets database/sql use the context path, which is what a cancelled
// query exercises.
func (c *fakeConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A map cloned per call, mirroring the capability set the real path clones
	// on every lock acquisition -- the single largest allocation site there.
	c.mu.Lock()
	if c.caps == nil {
		c.caps = make(map[string]string, 64)
		for i := range 64 {
			c.caps[string(rune('a'+i%26))+q[:min(len(q), 3)]+string(rune('0'+i%10))] = q
		}
	}
	cloned := maps.Clone(c.caps)
	c.mu.Unlock()
	return &fakeRows{cols: []string{"id", "name"}, n: 32, caps: cloned}, nil
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeStmt struct{ q string }

func (s *fakeStmt) Close() error                               { return nil }
func (s *fakeStmt) NumInput() int                              { return -1 }
func (s *fakeStmt) Exec([]driver.Value) (driver.Result, error) { return fakeResult{}, nil }
func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error) {
	return &fakeRows{cols: []string{"id"}, n: 8}, nil
}

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) { return 1, nil }
func (fakeResult) RowsAffected() (int64, error) { return 1, nil }

type fakeRows struct {
	cols []string
	n    int
	i    int
	caps map[string]string
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= r.n {
		return io.EOF
	}
	r.i++
	for k := range dest {
		if k == 0 {
			dest[k] = int64(r.i)
			continue
		}
		dest[k] = "row-" + string(rune('a'+r.i%26))
	}
	return nil
}

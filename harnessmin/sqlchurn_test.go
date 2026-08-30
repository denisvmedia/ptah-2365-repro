package harnessmin_test

// The victims are named. A reproduction on windows/amd64 under the repaired
// zombie report identified 95 objects in one span, and the runtime symbolized
// seven of them:
//
//	database/sql.(*Conn).closemuRUnlockCondReleaseConn-fm   x3
//	database/sql.(*driverConn).releaseConn-fm               x2
//	database/sql.(*Tx).closemuRUnlockRelease-fm             x1
//	database/sql.(*DB).beginDC.gowrap1                      x1
//
// All four are 16-byte method values, and two of them exist only on the
// transaction path: (*Tx).closemuRUnlockRelease-fm is built by (*Tx).grabConn,
// and beginDC.gowrap1 is the awaitDone goroutine beginDC starts when the
// context can be canceled. The five harnesses before this one issued queries
// against the pool and never opened a transaction, so neither was ever
// allocated. That is not a guess about what matters; it is the one allocation
// site the evidence names that the harness did not have.

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// begun counts transactions that actually started. A fake driver that refused
// to begin would leave every turn of the loop returning early, allocate none of
// the objects this file exists to allocate, and report a passing test -- which
// reads exactly like a harness that ran.
var begun atomic.Int64

// sqlChurn drives the connection and transaction lifecycle that produced the
// identified objects: BeginTx under a cancelable context so awaitDone is
// started, queries inside the transaction so grabConn builds its method value,
// a mix of commit and rollback, and the (*Conn) path beside it.
func sqlChurn(ctx context.Context, workers, txns int) {
	db, err := sql.Open("harnessfake", "churn")
	if err != nil {
		return
	}
	defer db.Close()

	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range txns {
				// A deadline rather than a count alone. The workload this
				// imitates runs for about two minutes and aborts 70% of the
				// way through, which under GOGC=1 is hundreds of collections
				// with a heap that keeps growing. A harness that finishes in
				// under a second never enters that regime, so the count is a
				// ceiling and the clock is what actually ends the run.
				if ctx.Err() != nil {
					return
				}
				runOneTx(ctx, db, i)
			}
		}(w)
	}
	wg.Wait()
}

// runOneTx is one turn of the lifecycle. A cancelable context is the point:
// beginDC starts its awaitDone goroutine only when ctx.Done() is non-nil, and
// that goroutine's func value is one of the objects that went missing.
func runOneTx(ctx context.Context, db *sql.DB, i int) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	tx, err := db.BeginTx(cctx, nil)
	if err != nil {
		return
	}
	begun.Add(1)

	for q := range 3 {
		rows, err := tx.QueryContext(cctx, "SELECT id, name FROM users WHERE round = ?", i+q)
		if err != nil {
			continue
		}
		drain(rows)
	}

	// Rollback and commit release the connection through different method
	// values, so alternate rather than picking one.
	if i%3 == 0 {
		_ = tx.Rollback()
	} else {
		_ = tx.Commit()
	}

	conn, err := db.Conn(cctx)
	if err != nil {
		return
	}
	if rows, err := conn.QueryContext(cctx, "SELECT id, name FROM users", i); err == nil {
		drain(rows)
	}
	_ = conn.Close()
}

func drain(rows *sql.Rows) {
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		_ = rows.Scan(&id, &name)
	}
}

// TestSQLChurn allocates the objects the reproduction named, in the shape that
// allocates them.
func TestSQLChurn(t *testing.T) {
	t.Parallel()

	workers := envInt("PTAH_2365_SQL_WORKERS", 24)
	txns := envInt("PTAH_2365_SQL_TXNS", 2000000)
	seconds := envInt("PTAH_2365_SQL_SECONDS", 45)
	if workers == 0 || txns == 0 || seconds == 0 {
		t.Skip("sql churn disabled")
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(seconds)*time.Second)
	defer cancel()

	before := begun.Load()
	sqlChurn(ctx, workers, txns)

	started := begun.Load() - before
	if started == 0 {
		t.Fatal("no transaction was begun, so none of the named objects were allocated")
	}
	t.Logf("transactions begun: %d over %ds with %d workers", started, seconds, workers)
}

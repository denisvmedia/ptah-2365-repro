package harness_test

// The candidate reduction. It keeps only the ingredients the field reports
// share and drops everything else ptah's migrator package does:
//
//   - modernc.org/sqlite databases opened and closed in a loop, which is the
//     one large body of unsafe in the binary;
//   - logging through slog.Default() around every step, because that Logger is
//     the object the aborts name in six of the eight cases that name anything;
//   - child processes, because the one abort with a running goroutine was
//     inside syscall.CreateProcess;
//   - context cancellation across a query, because modernc's interruptOnDone
//     goroutine was live in most dumps;
//   - parallel subtests and GOGC pressure, because the fault is a collector
//     one.
//
// Whether that set is sufficient is exactly what the workflow measures. It is
// not assumed here: the workflow runs this against a positive control that
// executes the real package, so a clean result from this harness means
// something only when the control reproduced in the same job.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/denisvmedia/ptah-2365-repro/witness"
)

// The child half: the test binary re-executes itself and the child dies, the
// way the migrator crash tests spawn a helper that exits 73.
const childEnv = "PTAH_2365_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
		os.Exit(73)
	}
	witness.InstallRoots()
	witness.CaptureLogger()

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			witness.CheckLogger()
			witness.Rotate(8)
			time.Sleep(time.Millisecond)
		}
	}()

	code := m.Run()
	close(stop)

	// A clean run has to prove it looked. If no cleanup ever fired, AddCleanup
	// measured nothing and "no reproduction" would be an empty statement.
	if witness.StaleSeen.Load() == 0 {
		fmt.Fprintln(os.Stderr, "REFUSED: no cleanup ever fired; the detector was inert")
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "detector: %d superseded cleanups fired, %d live collected, %d handler-word changes\n",
		witness.StaleSeen.Load(), witness.LiveCollected.Load(), witness.ShapeChanged.Load())
	os.Exit(code)
}

// Scale knobs, so the workflow can widen the workload without a code change.
// Defaults are sized for a few seconds per iteration on a hosted runner.
var (
	workers = envInt("PTAH_2365_WORKERS", 32)
	rounds  = envInt("PTAH_2365_ROUNDS", 16)
	inserts = envInt("PTAH_2365_INSERTS", 256)
)

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func TestWorkload(t *testing.T) {
	for i := range workers {
		t.Run(fmt.Sprintf("worker-%02d", i), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for round := range rounds {
				exerciseDatabase(t, filepath.Join(dir, fmt.Sprintf("r%02d.db", round)), round)
			}
			spawnChild(t)
		})
	}
}

func exerciseDatabase(t *testing.T, path string, round int) {
	t.Helper()
	slog.Info("Migrating up", "currentVersion", 0, "totalMigrations", 2, "round", round)

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE IF NOT EXISTS posts (id INTEGER PRIMARY KEY, user_id INTEGER, body TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_user ON posts(user_id)`,
	}
	for _, s := range stmts {
		slog.Info("Applying migration", "version", 1, "description", "000001_create_users.up.sql")
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := range inserts {
		if _, err := tx.Exec(`INSERT INTO users (name) VALUES (?)`, fmt.Sprintf("user-%d-%d", round, i)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	slog.Info("Applied migration", "version", 1, "description", "000001_create_users.up.sql")

	// Cancel across a query: this is what wakes modernc's interruptOnDone
	// goroutine, which was runnable in most of the field dumps.
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rows, err := db.QueryContext(ctx, `SELECT u.id, u.name, p.body FROM users u LEFT JOIN posts p ON p.user_id = u.id`)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
		}
	}()
	cancel()
	wg.Wait()

	slog.Info("All migrations applied successfully", "round", round)
	runtime.GC()
}

func spawnChild(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path: %v", err)
	}
	cmd := exec.Command(exe, "-test.run", "TestWorkload/worker-00")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	err = cmd.Run()
	var exitErr *exec.ExitError
	if err == nil {
		t.Fatal("child was expected to fail")
	}
	if ok := asExitError(err, &exitErr); !ok {
		t.Fatalf("child failed in an unexpected way: %v", err)
	}
	if got := exitErr.ExitCode(); got != 73 {
		t.Fatalf("child exit code = %d, want 73", got)
	}
}

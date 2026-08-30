package harnessmin_test

// The same candidate with modernc.org/sqlite removed: standard library only.
//
// Whether the C translation is a necessary ingredient is not something reading
// can settle -- it is the one large body of unsafe in the affected binary, and
// therefore the obvious suspect, but its arena sits ~26 TB from the Go heap on
// windows/amd64 (measured), 27 of 38 field aborts have no modernc goroutine
// live, and an audit of its allocator with ownership invariants came back
// clean. So this arm keeps everything else the field reports share -- logging
// through slog.Default(), child processes, cancellation, parallel subtests,
// file churn, GC pressure -- and drops the dependency.
//
// If this arm reproduces, the report upstream needs no third-party module at
// all. If only the sqlite arm does, that is the answer to the same question
// from the other side. Both are worth knowing; neither is assumed here.

import (
	"context"
	"errors"
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

	"github.com/denisvmedia/ptah-2365-repro/witness"
)

const childEnv = "PTAH_2365_CHILD"

var (
	workers = envInt("PTAH_2365_WORKERS", 32)
	rounds  = envInt("PTAH_2365_ROUNDS", 16)
	writes  = envInt("PTAH_2365_INSERTS", 256)
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

	if witness.StaleSeen.Load() == 0 {
		fmt.Fprintln(os.Stderr, "REFUSED: no cleanup ever fired; the detector was inert")
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "detector: %d superseded cleanups fired, %d live collected, %d handler-word changes\n",
		witness.StaleSeen.Load(), witness.LiveCollected.Load(), witness.ShapeChanged.Load())
	os.Exit(code)
}

func TestWorkload(t *testing.T) {
	for i := range workers {
		t.Run(fmt.Sprintf("worker-%02d", i), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for round := range rounds {
				exercise(t, filepath.Join(dir, fmt.Sprintf("r%02d.dat", round)), round)
			}
			spawnChild(t)
		})
	}
}

func exercise(t *testing.T, path string, round int) {
	t.Helper()
	slog.Info("Migrating up", "currentVersion", 0, "totalMigrations", 2, "round", round)

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	for i := range writes {
		slog.Info("Applying migration", "version", 1, "description", "000001_create_users.up.sql")
		if _, err := fmt.Fprintf(f, "user-%d-%d\n", round, i); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	slog.Info("Applied migration", "version", 1, "description", "000001_create_users.up.sql")

	// A goroutine woken by cancellation while the caller tears its work down,
	// which is the shape modernc's interruptOnDone had in the field dumps.
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		slog.Info("Rolling back migration", "version", 1, "description", "000001_create_users.down.sql")
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
	if !errors.As(err, &exitErr) {
		t.Fatalf("child failed in an unexpected way: %v", err)
	}
	if got := exitErr.ExitCode(); got != 73 {
		t.Fatalf("child exit code = %d, want 73", got)
	}
}

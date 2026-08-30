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
	rounds  = envInt("PTAH_2365_ROUNDS", 48)
	writes  = envInt("PTAH_2365_INSERTS", 256)
	// How many workers run the Go toolchain. The package the fault was seen in
	// builds a helper in a handful of its tests, not in all of them, and a build
	// per worker would dominate the iteration instead of being one ingredient
	// among several.
	builders = envInt("PTAH_2365_BUILDERS", 8)
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
	var arenas sync.WaitGroup
	for range envInt("PTAH_2365_ARENAS", 2) {
		arenas.Add(1)
		go arenaChurn(stop, &arenas)
	}
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
	arenas.Wait()

	if witness.StaleSeen.Load() == 0 {
		fmt.Fprintln(os.Stderr, "REFUSED: no cleanup ever fired; the detector was inert")
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "detector: %d superseded cleanups fired, %d live collected, %d handler-word changes, %d gc cycles\n",
		witness.StaleSeen.Load(), witness.LiveCollected.Load(), witness.ShapeChanged.Load(), witness.Exposure())
	os.Exit(code)
}

func TestWorkload(t *testing.T) {
	for i := range workers {
		t.Run(fmt.Sprintf("worker-%02d", i), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if i%max(1, workers/builders) == 0 {
				buildHelper(t, dir)
			}
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

// buildHelper runs the Go toolchain as a child process, which is what the
// package the fault was seen in does in its crash tests: it builds a helper
// binary per test. That is a far heavier child than re-executing this binary --
// hundreds of files read and written, a linker, and on a hosted Windows runner
// every one of those touched by real-time antivirus. Re-executing ourselves
// does not resemble it, and the difference is exactly the kind of thing this
// reduction is trying to find.
func buildHelper(t *testing.T, dir string) {
	t.Helper()
	// Distinct paths: on windows the .exe suffix hides a collision here, and on
	// every other platform the binary would land on top of its own source
	// directory.
	src := filepath.Join(dir, "helpersrc")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(helperSource), 0o644); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module helper\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write helper go.mod: %v", err)
	}
	out := filepath.Join(dir, "helperbin"+exeSuffix())
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = src
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("go build unavailable in this environment: %v: %s", err, b)
	}
	slog.Info("Applying migration", "version", 2, "description", "built helper")

	run := exec.Command(out)
	err := run.Run()
	var exitErr *exec.ExitError
	if err == nil {
		t.Fatal("helper was expected to fail")
	}
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper failed in an unexpected way: %v", err)
	}
	if got := exitErr.ExitCode(); got != 73 {
		t.Fatalf("helper exit code = %d, want 73", got)
	}
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

const helperSource = `package main

import "os"

func main() { os.Exit(73) }
`

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

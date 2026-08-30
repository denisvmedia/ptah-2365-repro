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

const (
	childEnv = "PTAH_2365_CHILD"
	// peerEnv marks a copy of this binary started by the parent to run
	// alongside it. The fault is observed under `go test ./...`, where the
	// machine is running one test binary per package at once -- several Go
	// runtimes, each with its own heap and collector, competing for the same
	// cores and the same antivirus. One process in isolation does not resemble
	// that, and a peer must not start peers of its own.
	peerEnv = "PTAH_2365_PEER"
)

var (
	workers = envInt("PTAH_2365_WORKERS", 8)
	rounds  = envInt("PTAH_2365_ROUNDS", 2)
	writes  = envInt("PTAH_2365_INSERTS", 32)
	// Files in the in-memory tree each worker parses per round. This is the
	// path the reproduction was actually executing, so it carries weight rather
	// than being one ingredient among many.
	provFiles = envInt("PTAH_2365_FILES", 8)
	// The registry churn is weighted deliberately. Two of the four control
	// reproductions were inside
	// TestRegisteredMigrationProvider_ConcurrentRegisterAndMigrations, which is
	// the only shape named twice, so it runs continuously here rather than once
	// per round.
	regWorkers = envInt("PTAH_2365_REG_WORKERS", 4)
	regIters   = envInt("PTAH_2365_REG_ITERS", 100)
	regLoops   = envInt("PTAH_2365_REG_LOOPS", 0)
	// The package the fault was seen in has 473 top-level tests, most with
	// subtests and t.Parallel. That is hundreds of testing.T values, cleanup
	// slices and framework goroutines, and it is a large part of what such a
	// binary allocates. Thirty-two workers do not resemble it.
	smallTests = envInt("PTAH_2365_SMALL_TESTS", 400)
	// Sized against the target rather than upward: the package this reduces
	// completes 137 GC cycles at GOGC=1, and a harness doing 92909 of them is
	// not a closer approximation, it is a different program.
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

	peers := startPeers()

	stop := make(chan struct{})

	// Keep the shape that reproduced twice running for the whole process, not
	// only when a worker happens to reach it.
	var registries sync.WaitGroup
	for range regLoops {
		registries.Add(1)
		go func() {
			defer registries.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				churnRegistry(regWorkers, regIters)
			}
		}()
	}

	var arenas sync.WaitGroup
	for range envInt("PTAH_2365_ARENAS", 1) {
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
	registries.Wait()

	// A peer that found the fault reports it in its own output and exits 97;
	// say so here too, or the parent's clean summary would bury it.
	for _, p := range peers {
		if err := p.Wait(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 97 {
				fmt.Fprintln(os.Stderr, "PTAH-2365 REPRODUCED in a peer process; see its output above")
				os.Exit(97)
			}
			fmt.Fprintf(os.Stderr, "peer exited unexpectedly: %v\n", err)
		}
	}

	if witness.StaleSeen.Load() == 0 {
		fmt.Fprintln(os.Stderr, "REFUSED: no cleanup ever fired; the detector was inert")
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "detector: %d superseded cleanups fired, %d live collected, %d handler-word changes, %d gc cycles\n",
		witness.StaleSeen.Load(), witness.LiveCollected.Load(), witness.ShapeChanged.Load(), witness.Exposure())
	os.Exit(code)
}

// startPeers runs additional copies of this binary concurrently, so the machine
// carries several Go runtimes at once the way `go test ./...` makes it.
func startPeers() []*exec.Cmd {
	if os.Getenv(peerEnv) != "" {
		return nil // a peer does not start peers
	}
	n := envInt("PTAH_2365_PEERS", 3)
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	out := make([]*exec.Cmd, 0, n)
	for range n {
		cmd := exec.Command(exe, "-test.count=1", "-test.timeout=20m")
		cmd.Env = append(os.Environ(), peerEnv+"=1")
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			continue
		}
		out = append(out, cmd)
	}
	return out
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

// TestManySmallParallel mirrors the shape of a package with hundreds of
// parallel tests rather than a handful of large ones: the framework's own
// per-test allocation is part of the workload being reduced.
func TestManySmallParallel(t *testing.T) {
	for i := range smallTests {
		t.Run(fmt.Sprintf("case-%03d", i), func(t *testing.T) {
			t.Parallel()
			t.Cleanup(func() {})
			r := &registry{}
			for k := range 8 {
				r.register(&entry{
					version:     int64(k),
					description: "small",
					up:          func() error { return nil },
					down:        func() error { return nil },
				})
			}
			if got := len(r.all()); got != 8 {
				t.Fatalf("registry length = %d, want 8", got)
			}
			slog.Info("Applied migration", "version", i, "description", "case")
		})
	}
}

func exercise(t *testing.T, path string, round int) {
	t.Helper()
	slog.Info("Migrating up", "currentVersion", 0, "totalMigrations", 2, "round", round)

	// Parse a tree of migration-shaped files: the code path the reproduction
	// was in when the collector aborted.
	fsys := buildFS(provFiles)
	loaded, err := loadAll(fsys)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, mf := range loaded {
		slog.Info("Applying migration", "version", 1, "description", mf.run())
	}

	// The other reproduction's path: concurrent producers appending pointers to
	// a growing slice while consumers clone and walk it.
	slog.Info("Registered migrations", "seen", churnRegistry(4, 100))

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

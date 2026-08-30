package harnessmin_test

// The path golang/go#44900 identified: StartProcess builds a handle-inheritance
// list and hands &fd[0] -- a Go-heap slice -- to UpdateProcThreadAttribute,
// which the Windows kernel writes into. That list is only built when handles are
// actually inherited, i.e. when the child gets extra handles or pipes. A child
// with no inherited handles never touches it.
//
// #44900 was fixed with runtime.KeepAlive(fd), and that KeepAlive is present in
// go1.27. The symptom persisting means this is a new instance of the same class
// -- a non-Go writer into Go-managed memory during a GC phase -- not the closed
// bug. Reproducing needs the path exercised, so this spawns children that
// inherit pipes, under GC pressure, with nothing outside the standard library.

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// spawnWithInheritedPipes runs a child of this test binary while passing it an
// extra inherited pipe, which is what makes StartProcess build the
// ProcThreadAttributeList handle list around a Go-heap slice.
func spawnWithInheritedPipes(ctx context.Context) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run", "TestWorkload/worker-00")
	cmd.Env = append(os.Environ(), childEnv+"=1")

	// ExtraFiles is exactly what populates the inherited-handle list.
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	defer pr.Close()
	defer pw.Close()
	cmd.ExtraFiles = []*os.File{pr}

	// A second and third pipe, so the list has several entries.
	pr2, pw2, _ := os.Pipe()
	pr3, pw3, _ := os.Pipe()
	if pr2 != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, pr2, pr3)
		defer pr2.Close()
		defer pw2.Close()
		defer pr3.Close()
		defer pw3.Close()
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go io.Copy(io.Discard, stdout)
	_ = cmd.Wait()
	return nil
}

// procAttrChurn spawns such children continuously until stop closes, several at
// a time, so the handle-list path runs while other goroutines allocate.
func procAttrChurn(stop <-chan struct{}, wg *sync.WaitGroup, parallel int) {
	defer wg.Done()
	sem := make(chan struct{}, parallel)
	var inner sync.WaitGroup
	for {
		select {
		case <-stop:
			inner.Wait()
			return
		case sem <- struct{}{}:
		}
		inner.Add(1)
		go func() {
			defer inner.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_ = spawnWithInheritedPipes(ctx)
		}()
	}
}

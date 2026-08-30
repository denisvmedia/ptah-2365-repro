package harnessmin_test

// The collision the mechanism needs, manufactured with the standard library.
//
// On Windows, asynchronous preemption is not a signal: runtime.preemptM
// suspends the target thread, captures its context with CONTEXT_CONTROL,
// consults PCDATA_UnsafePoint at the captured PC, rewrites SP and PC with
// PushCall, and resumes the thread with SetThreadContext. The guard against
// injecting inside an inlined write-barrier sequence is only as good as the
// captured context is exact.
//
// A goroutine in a call-free loop has no cooperative preemption point, so every
// preemption of it must take that path. Under GOGC=1 the mark phase -- write
// barriers armed -- covers most of the process's life. Together with allocation
// churn on other goroutines this maximizes suspensions that land on barrier
// sequences, which is where a lost barrier entry becomes a lost mark and a
// reachable object is swept.

import (
	"fmt"
	"sync/atomic"
)

var spinSink atomic.Uint64

// spin is deliberately call-free in its hot loop: no function calls means no
// preemption points, so only SuspendThread/SetThreadContext can stop it.
func spin(stop *atomic.Bool) {
	var x uint64 = 0x9E3779B97F4A7C15
	for !stop.Load() {
		for i := 0; i < 1<<14; i++ {
			x = x*6364136223846793005 + 1442695040888963407
		}
		spinSink.Store(x)
	}
}

// boxSingles allocates the exact shape found squatting in the corrupted Logger:
// a one-element []any backing array is 16 bytes whose first word is a *_type --
// an eface. fmt.Sprintf with one operand makes one per call.
func boxSingles(n int) int {
	total := 0
	for i := range n {
		s := fmt.Sprintf("%d", i*7)
		total += len(s)
	}
	return total
}

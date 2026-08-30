// Package witness holds the detectors shared by the candidate reductions of stokaro/ptah#2365 and the
// detectors that decide whether a run reproduced it.
//
// Two detectors, because the defect has two observable halves and the sweeper's
// own report is empty under Green Tea (golang/go#80799):
//
//   - liveCollected: runtime.AddCleanup on 16-byte two-pointer objects that a
//     package-level root keeps reachable. A cleanup firing for the generation a
//     root still holds means the collector freed a live object. Each cleanup
//     carries its slot and generation, so the ordinary case -- a later install
//     superseding an earlier object -- is counted separately instead of being
//     mistaken for a finding.
//
//   - handlerShape: slog.Default()'s Logger is 16 bytes and is exactly the
//     victim the field reports name. Its first word must stay the *itab of the
//     handler it was built with; an eface written over it leaves a *_type there
//     instead. Checking the word against the value captured at init catches the
//     reuse before the next call jumps through it.
//
// A run that reports "clean" must have exercised both. staleSeen is the proof
// that cleanups fire at all: a harness where AddCleanup never ran would report
// exactly the same clean result while measuring nothing, so the harness refuses
// that outcome rather than passing it off.
package witness

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync/atomic"
	"unsafe"
)

const roots = 4096

type victim struct { // 16 bytes: two pointer words, the shape of slog.Logger
	a *int
	b *string
}

type tag struct {
	slot int
	gen  uint64
}

var (
	slots [roots]atomic.Pointer[victim]
	gens  [roots]atomic.Uint64
	genC  atomic.Uint64

	StaleSeen     atomic.Int64 // superseded objects collected: the liveness proof
	LiveCollected atomic.Int64 // the finding
	ShapeChanged  atomic.Int64 // the finding, seen from the other side

	loggerWord0 uintptr
	loggerAddr  uintptr
)

// InstallRoots seeds the rooted population. Safe to call repeatedly; each call
// rotates one slot so superseded generations keep the cleanup path warm.
func InstallRoots() {
	for i := range roots {
		install(i)
	}
}

func install(i int) {
	g := genC.Add(1)
	v := &victim{a: new(int), b: new(string)}
	*v.a = i
	slots[i].Store(v)
	gens[i].Store(g)
	// The cleanup must not close over v, or v would never become unreachable.
	runtime.AddCleanup(v, func(t tag) {
		if gens[t.slot].Load() != t.gen {
			StaleSeen.Add(1)
			return
		}
		LiveCollected.Add(1)
		Report(fmt.Sprintf("LIVE OBJECT COLLECTED: slot=%d gen=%d is still the generation the root holds", t.slot, t.gen))
	}, tag{slot: i, gen: g})
}

// Rotate replaces n roots, so that cleanups keep firing for superseded
// generations for as long as the run lasts.
func Rotate(n int) {
	base := int(genC.Load())
	for k := range n {
		install((base*7 + k) % roots)
	}
}

// CaptureLogger records the identity and the itab word of the process-global
// default Logger. The address is kept as a uintptr so recording it does not
// keep the object reachable.
func CaptureLogger() {
	lg := slog.Default()
	if lg == nil {
		return
	}
	loggerAddr = uintptr(unsafe.Pointer(lg))
	loggerWord0 = *(*uintptr)(unsafe.Pointer(lg))
}

// CheckLogger reports if the default Logger's first word has changed. Under the
// defect it becomes a *_type -- the first word of an eface -- where an *itab
// belongs.
func CheckLogger() {
	if loggerAddr == 0 {
		return
	}
	lg := slog.Default()
	if uintptr(unsafe.Pointer(lg)) != loggerAddr {
		// Something legitimately replaced the default; re-anchor.
		CaptureLogger()
		return
	}
	if w := *(*uintptr)(unsafe.Pointer(lg)); w != loggerWord0 {
		ShapeChanged.Add(1)
		Report(fmt.Sprintf("SLOG HANDLER WORD CHANGED: logger=%#x itab was %#x now %#x", loggerAddr, loggerWord0, w))
	}
}

// Report prints every goroutine and leaves with a distinctive status so the
// workflow can tell a reproduction from an ordinary test failure.
func Report(what string) {
	buf := make([]byte, 1<<21)
	n := runtime.Stack(buf, true)
	fmt.Fprintf(os.Stderr, "\n*** PTAH-2365 REPRODUCED ***\n%s\n", what)
	os.Stderr.Write(buf[:n])
	os.Exit(97)
}

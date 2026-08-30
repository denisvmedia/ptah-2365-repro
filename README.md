# ptah-2365-repro

An attempt to reduce a windows/amd64 heap fault to something small enough to
hand to the Go tracker.

## What has been established

Measured on GitHub-hosted `windows-latest` with go1.27.0, against
`stokaro/ptah@9278f8566`.

**The collected crash reports were empty by construction.** `reportZombies`
reads the marks through `markBitsForBase`, which for a span with inline mark
bits returns the array `moveInlineMarks` cleared one line earlier. Every abort
in this fault is `elemsize=16`, which takes that path, so every report printed
one row per object, called all of them unmarked, identified nothing, and threw.
This is [golang/go#80799](https://github.com/golang/go/issues/80799).
[`zombiereport/`](zombiereport/) reproduces it deterministically in about forty
lines on three operating systems, and the fix that issue proposes is verified
there.

**With the report repaired, the fault names its objects.** Same collector, same
commit: 84 to 99 zombies per reproduction, symbolized as `database/sql` method
values and string headers — live objects, not stale memory.

**The direction of the failure is measured.** Enabling the collector's own
`doubleCheckGreenTea` asserts trips `marks don't match scans` a cycle earlier,
and printing which way the arrays differ gives `markedNotScanned` of 98 and
104, with `scannedNotMarked` of **zero** in both. Marks were set and never
consumed; nothing cleared them. External corruption predicts the opposite.

**Green Tea is required for the fault to appear.** `GOEXPERIMENT=nogreenteagc`
came back clean over 48 jobs against a same-day default-collector rate of 8.8%
(Fisher p = 0.03).

**One structural fact, unexplained.** In five independent reproductions the
divergence begins at object index **256** — where the zombies start in three,
and where the scans stop in two — across different spans, freeindexes and
object counts.

What is *not* established is the code path that produces this. Three
hypotheses were built and closed by reading: a P's span queue being dropped on
destroy, `tryAcquire` succeeding while `put` fails, and `work.spanqMask` being
set only on spill. See [`stokaro/ptah#2365`](https://github.com/stokaro/ptah/issues/2365)
for the reasoning.

## What is being reduced

A Go test binary aborts intermittently on GitHub-hosted `windows-latest` — 38
times in roughly 2,000 executions (~1.9%) between 2026-08-17 and 2026-08-29,
under go1.26.6, go1.26.7 and go1.27.0 alike. Four shapes, one defect:

| signature | count |
| --- | --- |
| `fatal error: found pointer to free object` (sweeper) | 28 |
| `unexpected fault address 0xffffffffffffffff` | 4 |
| `Exception 0xc0000005` with `ExceptionInformation[0]=8` (DEP) | 4 |
| `badPointer` during mark | 2 |

The size class is 16 bytes in 21 of the 28 sweeper reports, and six of the
eight aborts that name a victim die at `log/slog/logger.go:168`,
`l.Handler().Enabled(ctx, level)`, on the process-global `*slog.Logger`.
`unsafe.Sizeof(slog.Logger{})` is 16.

One dump was annotated with symbols. Rebuilding that binary and resolving the
addresses — four independent anchors agreeing on one ASLR slide, which is the
check that the rebuild matches — shows the DEP target is the start of
`go:funcdesc`, and the call site is:

```
MOVQ 0(AX), DX      ; DX = l.handler word0
MOVQ 0x18(DX), DX   ; DX = word0[0x18]
CALL DX
```

`0x18` is `ITab.Fun[0]`, a code address — and also `Type.Equal`, a func value.
The loaded value is a func value, so word0 was an `abi.Type`, not an `abi.ITab`.
`(*_type, data)` is an `eface`: **the 16 bytes of a `*slog.Logger` a live root
still pointed at were holding an `any`.**

Upstream context: the sweeper's own reports are empty here — every slot prints
`unmarked`, none is tagged `zombie`, and the object dump is never reached. That
is [golang/go#80799](https://github.com/golang/go/issues/80799), open with an
unmerged fix, and it is why none of the 38 aborts carries an object dump.

## Why this repository exists

It has not reproduced outside GitHub's hosted Windows runners. Measured, all
clean:

| where | runs |
| --- | --- |
| windows/amd64 container (Green Tea on, off, and uninstrumented) | ~300 |
| linux/amd64 **without** `-race` | 110+ |
| darwin/arm64 | ~230 |

At the observed rate those streaks have probability `0.981^300 ≈ 0.003` and
`0.981^230 ≈ 0.012`, so the hosted runner is measurably different rather than
merely luckier. A reducer therefore has to run there, which is what the
workflow does.

## Layout

- `harness/` — the candidate that keeps `modernc.org/sqlite`. Keeps only the
  ingredients the field reports share: `modernc.org/sqlite` opened and closed in a loop, logging through
  `slog.Default()` around every step, child processes, context cancellation
  across a query, parallel subtests, GC pressure.
- `harnessmin/` — the same candidate with the dependency removed: **standard
  library only**. Whether the C translation is a necessary ingredient cannot be
  settled by reading — it is the one large body of unsafe in the affected
  binary, but its arena sits ~26 TB from the Go heap on windows/amd64
  (measured), 27 of 38 field aborts have no modernc goroutine live, and an
  audit of its allocator with ownership invariants came back clean. So both are
  run and the difference is the answer. A reproduction here needs no
  third-party module at all, which is what the Go tracker wants.
- `witness/witness.go` — two detectors. `runtime.AddCleanup` on 4096 rooted
  16-byte two-pointer objects with generation tags, so a cleanup firing for the
  generation a root still holds is a collected live object; and a check that
  `slog.Default()`'s first word is still the `*itab` it was built with.
- `zombiereport/` — not a search for the rare fault but a deterministic
  measurement of what the runtime says when it finds one. It builds a zombie on
  purpose in a 16-byte scannable span and reads the report back. Under the
  default collector the report prints 496 rows, calls every one of them
  unmarked, identifies nothing, dumps no memory and aborts anyway; under
  `GOEXPERIMENT=nogreenteagc` the same zombie is named and hexdumped. Measured
  identically on linux/amd64, windows/amd64 and darwin/arm64. This is
  [golang/go#80799](https://github.com/golang/go/issues/80799), and it is why
  every crash dump collected for the fault carried no evidence by construction
  rather than by chance.
- `scripts/probe.ps1` — the iteration loop, shared by both arms.
- `.github/workflows/repro.yml` — `workflow_dispatch` and nightly.

## The control is the point

The workflow runs two arms in the same matrix: the candidate, and the real
package the fault was seen in, pinned to a commit whose CI run aborted.

Without the control, "the harness did not reproduce" and "this runner did not
reproduce anything today" are the same observation, and at ~1.9% most runs are
the second. The control is what separates them.

Both arms assert their own iteration count and refuse a run that produced no
test result, because a probe that ran nothing exits green and reads exactly
like a probe that found nothing.

## Running it

```
gh workflow run repro.yml -f iterations=80 -f arms=all
```

Roughly 80 iterations per arm gives a 78% chance of catching one occurrence if
the arm reproduces at the field rate; ~156 gives 95%. The nightly schedule
accumulates the record.

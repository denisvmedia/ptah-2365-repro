# Go 1.27 Windows exception-stack underflow reproducer

This program reproduces the `found pointer to free object` failure reported in
[golang/go#81238]. On affected Windows Server 2025/amd64 Intel hosts,
recovering a read access violation at address `0x118` writes below a 16 KiB
goroutine stack into an adjacent size-class-16 heap span.

The paired workflow builds stock Go 1.27.0 and exact [CL 824724 patch set 1]
from the same toolchain on the same runner. In the [confirmed comparison], both
binaries changed the adjacent span at `stack_lo+0x48` and `stack_lo+0x20`,
then failed in `runtime.(*mspan).reportZombies`. The CL did not emit its new
guard diagnostic.

`REPRO_TAIL` records the corruption before the later GC report, independently
of the reporting defect in [golang/go#80799]. A clean run on the mixed runner
pool is inconclusive.

## Run

```powershell
$env:GODEBUG = 'adaptivestackstart=0'
$env:GOGC = 'off'
$env:GOMEMLIMIT = 'off'
$env:GOTRACEBACK = 'system'
go build -trimpath -buildvcs=false -o repro.exe .

.\repro.exe panic 2> panic.log
.\repro.exe fault 2> fault.log
```

`panic` is the negative control. The GitHub workflow applies
[`cl824724.patch`](cl824724.patch) through `go build -overlay` and classifies
the direct tail witness rather than the GC report.

[CL 824724 patch set 1]: https://go-review.googlesource.com/c/go/+/824724/1
[confirmed comparison]: https://github.com/denisvmedia/ptah-2365-repro/actions/runs/33403553197/job/99525466135
[golang/go#81238]: https://github.com/golang/go/issues/81238
[golang/go#80799]: https://github.com/golang/go/issues/80799

# Go 1.27 Windows exception-stack underflow reproducer

This standalone program reproduces the `found pointer to free object` failure
seen in Ptah. On affected Windows Server 2025/amd64 hosts, recovering a read
access violation at address `0x118` writes below a 16 KiB goroutine stack into
an adjacent size-class-16 heap span. The next GC detects corrupted mark bits.

Tested with Go 1.27.0 on GitHub-hosted `windows-latest` runners with Intel AMX
XSTATE enabled. The runner pool is mixed, so a non-AMX attempt exits cleanly.

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

`panic` is the negative control and exits 0 without changing the adjacent page.
On an affected host, `fault` exits 2 with `fatal error: found pointer to free
object` from `runtime.(*mspan).reportZombies`.

The program and workflow use stock Go. [golang/go#80799] only makes the fatal
report reread cleared mark bits; it is not applied here. A [paired diagnostic
run] showed the same abort with stock Go and with the report-only repair.

The module uses only the standard library. It contains no Ptah source,
dependency, database, filesystem/process workload, or test harness.

[golang/go#80799]: https://github.com/golang/go/issues/80799
[paired diagnostic run]: https://github.com/denisvmedia/ptah-2365-repro/actions/runs/33350325788

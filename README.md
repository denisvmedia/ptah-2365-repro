# ptah-2365-repro

Minimal GitHub-hosted Windows reproducer for the intermittent Go runtime
corruption seen in Ptah.

Each of 40 jobs checks out [the failing Ptah commit][ptah], builds
`./migration/migrator` once with Go 1.27.0, and runs that fresh test
binary once from its package directory with `GOGC=1`.

`repro.go` applies only the report repair from [golang/go#80799] and proves it
with a deliberate zombie before measuring Ptah. It does not change the fault.

Captured data below `stack.lo` decodes as `runtime.systemstack` and
`runtime.sigtrampgo` frame data, with a low-water mark at `stack.lo-0x20`.

Run **reproduce Windows runtime corruption** from the Actions tab.
`CORRUPTION` jobs fail by design; anything other than recognized corruption
or one clean `PASS` is `REFUSED`.

[golang/go#80799]: https://github.com/golang/go/issues/80799
[ptah]: https://github.com/stokaro/ptah/commit/9278f8566c88c3bf949bd3c5cd22fad1d37006b4

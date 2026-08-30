# One iteration loop, shared by both arms.
#
# The counts are asserted rather than assumed. A probe that ran nothing exits
# green and reads exactly like a probe that found nothing, which is the failure
# this whole exercise exists to avoid: every abort in the field was invisible
# for the same reason.
param(
  [Parameter(Mandatory = $true)][ValidateSet('candidate', 'candidate-min', 'control')][string]$Kind,
  # A -test.run pattern, so the control arm can be bisected: the fault
  # reproduces on the first iteration under GOGC=1, which makes halving the
  # package's test list a minutes-long question rather than a nightly one.
  [string]$Filter = ''
)

# Continue, deliberately. Under Stop a native command's stderr merged with 2>&1
# arrives as ErrorRecords that end the script, so `go test` printing its first
# diagnostic would look like the probe itself failing.
$ErrorActionPreference = 'Continue'

# PowerShell splits a native command's argument at the dot in `-test.count=1`
# and the binary sees `-test`, which it rejects with its usage text and exit 2.
# Splatting an array passes each element through verbatim, and Standard argument
# passing keeps PowerShell from re-quoting them on the way.
$PSNativeCommandArgumentPassing = 'Standard'
$testArgs = @('-test.count=1', '-test.timeout=25m')
if ($Filter) { $testArgs += "-test.run=$Filter" }

$iterations = if ($env:ITERATIONS) { [int]$env:ITERATIONS } else { 80 }
if ($iterations -lt 1) {
  "refused: iterations must be at least 1, got $iterations" | Out-File verdict.txt
  Write-Host "::error::iterations must be at least 1, got $iterations"
  exit 1
}

# runDir matters. `go test` runs a package's tests with the working directory
# set to that package, and tests rely on it: the control arm reads ..\..\docs
# and shells out to `go build`, and running its binary from anywhere else fails
# both. That is an artefact of the probe, not a property of the fault, and the
# first run of this workflow spent an arm discovering it.
switch ($Kind) {
  'candidate'     { $buildDir = '.';        $pkg = './harness/';            $runDir = 'harness' }      # with modernc.org/sqlite
  'candidate-min' { $buildDir = '.';        $pkg = './harnessmin/';         $runDir = 'harnessmin' }   # standard library only
  'control'       { $buildDir = 'upstream'; $pkg = './migration/migrator/'; $runDir = 'upstream/migration/migrator' }
}

# Logs and the verdict belong at the workspace root, where the artifact step
# looks for them, and not wherever the binary happens to run.
$root = $PWD

Push-Location $buildDir
go test -c -o "$PWD\probe.test.exe" $pkg 2>&1 | Tee-Object -FilePath "$PWD\build.log"
if ($LASTEXITCODE -ne 0) {
  Pop-Location
  "refused: could not build $pkg" | Out-File (Join-Path $root 'verdict.txt')
  Write-Host "::error::could not build $pkg"
  exit 1
}
$bin = "$PWD\probe.test.exe"
Pop-Location

# Smoke: one run before the loop, so a binary that cannot be invoked is
# reported as that rather than as a strange first iteration. Every abort this
# repository is chasing went unexplained because something reported without
# running; the probe should not join them.
$signatures = 'PTAH-2365 REPRODUCED|LIVE OBJECT COLLECTED|SLOG HANDLER WORD CHANGED|found pointer to free object|marked free object|unexpected fault address|Exception 0xc0000005|fatal error:'

$smoke = Join-Path $root 'smoke.log'
Push-Location $runDir
& $bin @testArgs 2>&1 | Tee-Object -FilePath $smoke | Out-Null
$smokeStatus = $LASTEXITCODE
Pop-Location
# The signature first. A run that aborts in the collector prints a stack trace
# and never reaches PASS, so asking "did it produce a test result" before asking
# "did it reproduce" classifies the thing being hunted as a broken probe. That
# is exactly what happened to the first bisection branch, and the evidence was
# thrown away with it.
if (Select-String -Path $smoke -Pattern $signatures -Quiet) {
  "REPRODUCED on the smoke run, before iteration 1" | Out-File (Join-Path $root 'verdict.txt')
  Copy-Item -LiteralPath $smoke -Destination (Join-Path $root 'probe-smoke.log') -Force
  Write-Host "::error::reproduced on the smoke run; the artifact holds the full output"
  Get-Content $smoke -Tail 80
  exit 1
}
if (-not (Select-String -Path $smoke -Pattern '^(PASS|ok|FAIL|---)' -Quiet)) {
  "refused: the test binary produced no test result when invoked" | Out-File (Join-Path $root 'verdict.txt')
  Copy-Item -LiteralPath $smoke -Destination (Join-Path $root 'probe-smoke.log') -Force
  Write-Host "::error::the test binary produced no test result; see smoke.log"
  Get-Content $smoke -Tail 20
  exit 1
}
# An arm has to be able to pass at all. A control that fails every iteration for
# reasons of its own can never be the positive control it exists to be, and the
# failure would be indistinguishable from the one being hunted only by reading
# each log.
if ($smokeStatus -ne 0) {
  "refused: the arm does not pass cleanly even once (exit $smokeStatus)" | Out-File (Join-Path $root 'verdict.txt')
  Write-Host "::error::the arm does not pass cleanly even once; see smoke.log"
  Select-String -Path $smoke -Pattern '^(--- FAIL|FAIL)' | Select-Object -First 10 | ForEach-Object { $_.Line }
  exit 1
}
Remove-Item $smoke -ErrorAction SilentlyContinue

Write-Host "probing $Kind ($pkg) for $iterations iterations, GOGC=$env:GOGC, filter=$Filter"

$ran = 0

for ($i = 1; $i -le $iterations; $i++) {
  $log = Join-Path $root "probe-$i.log"

  # Both reproductions so far landed on iteration 1 of 80. Under a uniform
  # per-iteration rate that pair has probability (1/80)^2, so the first
  # execution of a freshly written binary is where the fault concentrates --
  # which is also the one moment a hosted runner's real-time antivirus opens the
  # file to scan it, and the container that never reproduced in 3110 runs has no
  # antivirus at all. So give every iteration its own freshly written image
  # rather than re-running one file eighty times. Copying is far cheaper than
  # rebuilding and produces the same thing: a path the scanner has not seen.
  $iterBin = Join-Path $runDir "probe-iter-$i.exe"
  Copy-Item -LiteralPath $bin -Destination $iterBin -Force

  Push-Location $runDir
  & $iterBin @testArgs 2>&1 | Tee-Object -FilePath $log | Out-Null
  $status = $LASTEXITCODE
  Pop-Location
  Remove-Item -LiteralPath $iterBin -Force -ErrorAction SilentlyContinue
  $ran++

  if (Select-String -Path $log -Pattern $signatures -Quiet) {
    "REPRODUCED on iteration $i of $iterations (exit $status)" | Out-File (Join-Path $root 'verdict.txt')
    Write-Host "::error::$Kind reproduced on iteration $i; the artifact holds the full output"
    Get-Content $log -Tail 80
    exit 1
  }

  # A clean iteration has to look like one.
  if (-not (Select-String -Path $log -Pattern '^(PASS|ok|FAIL|---)' -Quiet)) {
    "refused: iteration $i produced no test result (exit $status)" | Out-File (Join-Path $root 'verdict.txt')
    Write-Host "::error::iteration $i produced no test result"
    Get-Content $log -Tail 40
    exit 1
  }
  if ($status -ne 0) {
    "suspect: iteration $i exited $status with a test result but no known signature" | Out-File (Join-Path $root 'verdict.txt')
    Write-Host "::error::iteration $i exited $status unexpectedly"
    Get-Content $log -Tail 40
    exit 1
  }
  Remove-Item $log -ErrorAction SilentlyContinue
  Write-Host "iteration $i of ${iterations}: clean"
}

if ($ran -ne $iterations) {
  "refused: ran $ran iterations of $iterations" | Out-File (Join-Path $root 'verdict.txt')
  Write-Host "::error::ran $ran iterations of $iterations"
  exit 1
}
"not reproduced in $ran iterations" | Out-File (Join-Path $root 'verdict.txt')
Write-Host "$Kind not reproduced in $ran iterations"

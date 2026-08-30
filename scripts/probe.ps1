# One iteration loop, shared by both arms.
#
# The counts are asserted rather than assumed. A probe that ran nothing exits
# green and reads exactly like a probe that found nothing, which is the failure
# this whole exercise exists to avoid: every abort in the field was invisible
# for the same reason.
param(
  [Parameter(Mandatory = $true)][ValidateSet('candidate', 'control')][string]$Kind
)

# Continue, deliberately. Under Stop a native command's stderr merged with 2>&1
# arrives as ErrorRecords that end the script, so `go test` printing its first
# diagnostic would look like the probe itself failing.
$ErrorActionPreference = 'Continue'

$iterations = if ($env:ITERATIONS) { [int]$env:ITERATIONS } else { 80 }
if ($iterations -lt 1) {
  "refused: iterations must be at least 1, got $iterations" | Out-File verdict.txt
  Write-Host "::error::iterations must be at least 1, got $iterations"
  exit 1
}

if ($Kind -eq 'candidate') {
  $buildDir = '.'
  $pkg = './harness/'
} else {
  $buildDir = 'upstream'
  $pkg = './migration/migrator/'
}

Push-Location $buildDir
go test -c -o "$PWD\probe.test.exe" $pkg 2>&1 | Tee-Object -FilePath "$PWD\build.log"
if ($LASTEXITCODE -ne 0) {
  Pop-Location
  "refused: could not build $pkg" | Out-File verdict.txt
  Write-Host "::error::could not build $pkg"
  exit 1
}
$bin = "$PWD\probe.test.exe"
Pop-Location

Write-Host "probing $Kind ($pkg) for $iterations iterations, GOGC=$env:GOGC"

$ran = 0
$signatures = 'PTAH-2365 REPRODUCED|LIVE OBJECT COLLECTED|SLOG HANDLER WORD CHANGED|found pointer to free object|marked free object|unexpected fault address|Exception 0xc0000005|fatal error:'

for ($i = 1; $i -le $iterations; $i++) {
  $log = "probe-$i.log"
  & $bin -test.count=1 -test.timeout=25m 2>&1 | Tee-Object -FilePath $log | Out-Null
  $status = $LASTEXITCODE
  $ran++

  if (Select-String -Path $log -Pattern $signatures -Quiet) {
    "REPRODUCED on iteration $i of $iterations (exit $status)" | Out-File verdict.txt
    Write-Host "::error::$Kind reproduced on iteration $i; the artifact holds the full output"
    Get-Content $log -Tail 80
    exit 1
  }

  # A clean iteration has to look like one.
  if (-not (Select-String -Path $log -Pattern '^(PASS|ok|FAIL|---)' -Quiet)) {
    "refused: iteration $i produced no test result (exit $status)" | Out-File verdict.txt
    Write-Host "::error::iteration $i produced no test result"
    Get-Content $log -Tail 40
    exit 1
  }
  if ($status -ne 0) {
    "suspect: iteration $i exited $status with a test result but no known signature" | Out-File verdict.txt
    Write-Host "::error::iteration $i exited $status unexpectedly"
    Get-Content $log -Tail 40
    exit 1
  }
  Remove-Item $log -ErrorAction SilentlyContinue
  Write-Host "iteration $i of ${iterations}: clean"
}

if ($ran -ne $iterations) {
  "refused: ran $ran iterations of $iterations" | Out-File verdict.txt
  Write-Host "::error::ran $ran iterations of $iterations"
  exit 1
}
"not reproduced in $ran iterations" | Out-File verdict.txt
Write-Host "$Kind not reproduced in $ran iterations"

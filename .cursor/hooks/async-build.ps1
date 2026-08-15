$ErrorActionPreference = "Continue"
try { $null = [Console]::In.ReadToEnd() } catch {}

$root = "e:\gitee\k8man"
$marker = Join-Path $root "_TRIGGER_BUILD.txt"
$lock = Join-Path $root "_build_started.lock"
$log = Join-Path $root "hook_launch.log"

function Log($m) {
  Add-Content -Path $log -Value ("[{0}] {1}" -f (Get-Date -Format o), $m) -Encoding utf8
}

Log "hook invoked"
if (-not (Test-Path $marker)) {
  Log "no trigger marker; skip"
  Write-Output "{}"
  exit 0
}

if (Test-Path $lock) {
  $age = (Get-Date) - (Get-Item $lock).LastWriteTime
  if ($age.TotalMinutes -lt 15) {
    Log "lock present; skip duplicate"
    Write-Output "{}"
    exit 0
  }
}

"started $(Get-Date -Format o)" | Set-Content -Path $lock -Encoding utf8
Remove-Item $marker -Force -ErrorAction SilentlyContinue
Log "launching _run_build.bat via Start-Process"

Start-Process -FilePath "cmd.exe" `
  -ArgumentList "/c","_run_build.bat" `
  -WorkingDirectory $root `
  -WindowStyle Hidden

Log "Start-Process returned"
Write-Output "{}"
exit 0

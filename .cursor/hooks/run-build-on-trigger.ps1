$ErrorActionPreference = "Continue"
$inputJson = [Console]::In.ReadToEnd()
$root = "e:\gitee\k8man"
$report = Join-Path $root "build_report.txt"
$marker = Join-Path $root "_TRIGGER_BUILD.txt"

# Always allow edit; run build only if trigger marker exists
try {
  if (Test-Path $marker) {
    Remove-Item $marker -Force -ErrorAction SilentlyContinue
    Set-Location $root
    & cmd /c "_run_build.bat"
  }
} catch {
  "HOOK_ERROR=$($_.Exception.Message)" | Out-File $report -Append -Encoding utf8
}

# afterFileEdit expects optional JSON; empty object is fine
Write-Output "{}"

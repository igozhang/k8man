$ErrorActionPreference = "Continue"
# 在 %TEMP% 中打包，不把 webdist 回写到 git 工作区。
$Root = "e:\gitee\k8man"
$BuildRoot = Join-Path $env:TEMP ("k8man-build-" + $PID)
$report = Join-Path $env:TEMP ("k8man-build-" + $PID + "-report.txt")
$env:GOPROXY = "https://goproxy.cn,direct"
$env:GOSUMDB = "sum.golang.google.cn"

function Append($s) { Add-Content -Path $report -Value $s -Encoding utf8 }

"=== BUILD IN TEMP $(Get-Date -Format o) ===" | Out-File $report -Encoding utf8
Append "ROOT=$Root"
Append "BUILD_ROOT=$BuildRoot"

if (Test-Path $BuildRoot) { Remove-Item $BuildRoot -Recurse -Force }
New-Item -ItemType Directory -Path $BuildRoot | Out-Null

# 复制源码到 TEMP（排除 node_modules / dist / .git）
$robolog = Join-Path $env:TEMP "k8man-robo-$PID.log"
& robocopy $Root $BuildRoot /E /NFL /NDL /NJH /NJS /nc /ns /np `
  /XD .git web\node_modules web\dist data cmd\server\webdist\assets `
  /XF *.exe *.log k8man.exe _TRIGGER_BUILD.txt _build_started.lock | Out-Null
Append "ROBOCOPY_EXIT=$LASTEXITCODE"

Set-Location (Join-Path $BuildRoot "web")
Append "--- NPM INSTALL ---"
$npmI = & npm install --no-audit --no-fund 2>&1 | Out-String
Append $npmI
Append "NPM_INSTALL_EXIT=$LASTEXITCODE"

Append "--- NPM BUILD ---"
$npmB = & npm run build 2>&1 | Out-String
Append $npmB
if ($LASTEXITCODE -ne 0) {
  Append "NPM_BUILD=failure"
  Copy-Item $report (Join-Path $Root "build_report.txt") -Force
  exit 1
}
Append "NPM_BUILD=success"

$webdist = Join-Path $BuildRoot "cmd\server\webdist"
New-Item -ItemType Directory -Force -Path $webdist | Out-Null
Get-ChildItem $webdist -Force -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
Copy-Item (Join-Path $BuildRoot "web\dist\*") $webdist -Recurse -Force
Append "COPY_WEBDIST_IN_TEMP=success"

Set-Location $BuildRoot
Append "--- GO BUILD ---"
$out = & go build -o k8man.exe ./cmd/server 2>&1 | Out-String
Append $out
if ($LASTEXITCODE -eq 0) {
  Append "GO_BUILD=success"
  Append "BINARY=$BuildRoot\k8man.exe"
} else {
  Append "GO_BUILD=failure"
}

Append "NOTE=webdist was NOT copied back to $Root (git tree untouched)"
Append "PIPELINE_DONE"
Append "=== END $(Get-Date -Format o) ==="

# 仅回写报告，不回写 webdist
Copy-Item $report (Join-Path $Root "build_report.txt") -Force -ErrorAction SilentlyContinue

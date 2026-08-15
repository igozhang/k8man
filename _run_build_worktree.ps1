$ErrorActionPreference = "Continue"
$Root = "e:\gitee\k8man"
$Worktree = "e:\gitee\k8man-build-wt"
$LogRoot = $Root

# Prefer isolated worktree; fall back to main repo
$useWorktree = $false
try {
  Push-Location $Root
  if (-not (Test-Path $Worktree)) {
    $branch = "build/isolated-$(Get-Date -Format 'yyyyMMddHHmmss')"
    git worktree add -b $branch $Worktree HEAD 2>&1 | Out-String | Out-File "$Root\_wt_create.log" -Encoding utf8
  }
  if (Test-Path "$Worktree\.git") { $useWorktree = $true }
  # Sync go.sum from main if worktree missing/outdated
  if ($useWorktree -and (Test-Path "$Root\go.sum")) {
    Copy-Item "$Root\go.sum" "$Worktree\go.sum" -Force
  }
  if ($useWorktree -and (Test-Path "$Root\go.mod")) {
    Copy-Item "$Root\go.mod" "$Worktree\go.mod" -Force
  }
} catch {
  "worktree setup error: $_" | Out-File "$Root\_wt_create.log" -Append -Encoding utf8
} finally {
  Pop-Location -ErrorAction SilentlyContinue
}

$BuildRoot = if ($useWorktree) { $Worktree } else { $Root }
$report = Join-Path $BuildRoot "build_report.txt"
$env:GOPROXY = "https://goproxy.cn,direct"
$env:GOSUMDB = "sum.golang.google.cn"
Set-Location $BuildRoot

"=== BUILD PIPELINE START $(Get-Date -Format o) ===" | Out-File $report -Encoding utf8
function Append($s) { Add-Content -Path $report -Value $s -Encoding utf8 }
Append "BUILD_ROOT=$BuildRoot"
Append "USE_WORKTREE=$useWorktree"
Append "GOPROXY=$env:GOPROXY"
Append "GOSUMDB=$env:GOSUMDB"

Append "--- GO BUILD 1 ---"
$out1 = & go build -o k8man.exe ./cmd/server 2>&1 | Out-String
Append $out1
if ($LASTEXITCODE -eq 0) { Append "GO_BUILD1=success" } else { Append "GO_BUILD1=failure"; Append "GO_BUILD1_ERROR=$out1" }

Append "--- NPM INSTALL ---"
Set-Location (Join-Path $BuildRoot "web")
$npmI = & npm install 2>&1 | Out-String
Append $npmI
Append "NPM_INSTALL_EXIT=$LASTEXITCODE"

Append "--- NPM BUILD ---"
$npmB = & npm run build 2>&1 | Out-String
Append $npmB
if ($LASTEXITCODE -eq 0) {
  Append "NPM_BUILD=success"
  Set-Location $BuildRoot
  $webdist = Join-Path $BuildRoot "cmd\server\webdist"
  if (-not (Test-Path $webdist)) { New-Item -ItemType Directory -Path $webdist | Out-Null }
  Copy-Item -Path (Join-Path $BuildRoot "web\dist\*") -Destination $webdist -Recurse -Force
  Append "COPY=success"
} else {
  Append "NPM_BUILD=failure"
  Append "NPM_ERROR=$npmB"
  Append "COPY=skipped"
  Set-Location $BuildRoot
}

Append "--- GO BUILD 2 ---"
$out2 = & go build -o k8man.exe ./cmd/server 2>&1 | Out-String
Append $out2
if ($LASTEXITCODE -eq 0) { Append "GO_BUILD2=success" } else { Append "GO_BUILD2=failure"; Append "GO_BUILD2_ERROR=$out2" }

Append "--- KEY FILES ---"
$exe = Join-Path $BuildRoot "k8man.exe"
if (Test-Path $exe) {
  $fi = Get-Item $exe
  Append "k8man.exe size=$($fi.Length) time=$($fi.LastWriteTime)"
}
$webDistDir = Join-Path $BuildRoot "web\dist"
if (Test-Path $webDistDir) {
  Get-ChildItem $webDistDir -Recurse -File | ForEach-Object {
    $rel = $_.FullName.Substring($webDistDir.Length).TrimStart('\','/')
    Append "web/dist/$rel size=$($_.Length)"
  }
}
$webdistDir2 = Join-Path $BuildRoot "cmd\server\webdist"
if (Test-Path $webdistDir2) {
  Get-ChildItem $webdistDir2 -Recurse -File | ForEach-Object {
    $rel = $_.FullName.Substring($webdistDir2.Length).TrimStart('\','/')
    Append "cmd/server/webdist/$rel size=$($_.Length)"
  }
}

# Copy artifacts back to main repo if worktree differs
if ($useWorktree -and ($BuildRoot -ne $Root)) {
  Append "--- COPY BACK TO MAIN ---"
  try {
    if (Test-Path $exe) {
      Copy-Item $exe (Join-Path $Root "k8man.exe") -Force
      Append "copied k8man.exe"
    }
    $dstWebdist = Join-Path $Root "cmd\server\webdist"
    if (-not (Test-Path $dstWebdist)) { New-Item -ItemType Directory -Path $dstWebdist -Force | Out-Null }
    if (Test-Path $webDistDir) {
      $dstWeb = Join-Path $Root "web\dist"
      if (-not (Test-Path $dstWeb)) { New-Item -ItemType Directory -Path $dstWeb -Force | Out-Null }
      Copy-Item (Join-Path $webDistDir "*") $dstWeb -Recurse -Force
      Append "copied web/dist"
    }
    if (Test-Path $webdistDir2) {
      Copy-Item (Join-Path $webdistDir2 "*") $dstWebdist -Recurse -Force
      Append "copied cmd/server/webdist"
    }
    Copy-Item $report (Join-Path $Root "build_report.txt") -Force
    Append "copied build_report.txt"
    if (Test-Path (Join-Path $BuildRoot "go.sum")) {
      Copy-Item (Join-Path $BuildRoot "go.sum") (Join-Path $Root "go.sum") -Force
      Append "synced go.sum"
    }
    Append "COPY_BACK=success"
  } catch {
    Append "COPY_BACK=failure $_"
  }
}

Append "PIPELINE_DONE"
Append "=== END $(Get-Date -Format o) ==="

# Also mirror report to main always
Copy-Item $report (Join-Path $Root "build_report.txt") -Force -ErrorAction SilentlyContinue

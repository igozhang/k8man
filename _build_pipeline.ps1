$ErrorActionPreference = "Continue"
$env:GOPROXY = "https://goproxy.cn,direct"
$env:GOSUMDB = "sum.golang.google.cn"
Set-Location e:\gitee\k8man
$report = "e:\gitee\k8man\build_report.txt"
"=== BUILD PIPELINE START $(Get-Date -Format o) ===" | Out-File $report -Encoding utf8

function Append($s) { Add-Content -Path $report -Value $s -Encoding utf8 }

# Step 1: go build
Append "--- GO BUILD 1 ---"
$out1 = & go build -o k8man.exe ./cmd/server 2>&1 | Out-String
Append $out1
if ($LASTEXITCODE -eq 0) {
  Append "GO_BUILD1=success"
} else {
  Append "GO_BUILD1=failure"
  Append "GO_BUILD1_ERROR=$out1"
}

# Step 2-3: npm
Append "--- NPM INSTALL ---"
Set-Location e:\gitee\k8man\web
$npmI = & npm install 2>&1 | Out-String
Append $npmI
$npmIExit = $LASTEXITCODE
Append "NPM_INSTALL_EXIT=$npmIExit"

Append "--- NPM BUILD ---"
$npmB = & npm run build 2>&1 | Out-String
Append $npmB
if ($LASTEXITCODE -eq 0) {
  Append "NPM_BUILD=success"
  Set-Location e:\gitee\k8man
  if (-not (Test-Path "cmd\server\webdist")) { New-Item -ItemType Directory -Path "cmd\server\webdist" | Out-Null }
  Copy-Item -Path "web\dist\*" -Destination "cmd\server\webdist\" -Recurse -Force
  Append "COPY=success"
} else {
  Append "NPM_BUILD=failure"
  Append "NPM_ERROR=$npmB"
  Append "COPY=skipped"
  Set-Location e:\gitee\k8man
}

# Step 4: rebuild go
Append "--- GO BUILD 2 ---"
$out2 = & go build -o k8man.exe ./cmd/server 2>&1 | Out-String
Append $out2
if ($LASTEXITCODE -eq 0) {
  Append "GO_BUILD2=success"
} else {
  Append "GO_BUILD2=failure"
  Append "GO_BUILD2_ERROR=$out2"
}

Append "--- KEY FILES ---"
if (Test-Path "e:\gitee\k8man\k8man.exe") {
  $fi = Get-Item "e:\gitee\k8man\k8man.exe"
  Append "k8man.exe size=$($fi.Length) time=$($fi.LastWriteTime)"
}
if (Test-Path "e:\gitee\k8man\web\dist") {
  Get-ChildItem "e:\gitee\k8man\web\dist" -Recurse -File | ForEach-Object { Append "web/dist/$($_.FullName.Substring('e:\gitee\k8man\web\dist\'.Length))" }
}
if (Test-Path "e:\gitee\k8man\cmd\server\webdist") {
  Get-ChildItem "e:\gitee\k8man\cmd\server\webdist" -Recurse -File | ForEach-Object { Append "cmd/server/webdist/$($_.FullName.Substring('e:\gitee\k8man\cmd\server\webdist\'.Length))" }
}
Append "PIPELINE_DONE"
Append "=== END $(Get-Date -Format o) ==="

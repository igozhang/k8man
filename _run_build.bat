@echo off
setlocal EnableDelayedExpansion
REM 在 %%TEMP%% 中打包，不修改 git 工作区中的 webdist
set GOPROXY=https://goproxy.cn,direct
set GOSUMDB=sum.golang.google.cn
set MAIN=e:\gitee\k8man
set BUILD_ROOT=%TEMP%\k8man-build-%RANDOM%
set REPORT=%TEMP%\k8man-build-report-%RANDOM%.txt

echo === BUILD IN TEMP %DATE% %TIME% > "%REPORT%"
echo BUILD_ROOT=%BUILD_ROOT% >> "%REPORT%"

if exist "%BUILD_ROOT%" rmdir /s /q "%BUILD_ROOT%"
mkdir "%BUILD_ROOT%"

robocopy "%MAIN%" "%BUILD_ROOT%" /E /NFL /NDL /NJH /NJS /nc /ns /np /XD .git web\node_modules web\dist data cmd\server\webdist\assets /XF *.exe *.log k8man.exe _TRIGGER_BUILD.txt _build_started.lock >> "%REPORT%" 2>&1

cd /d "%BUILD_ROOT%\web"
echo --- NPM INSTALL --- >> "%REPORT%"
call npm install --no-audit --no-fund >> "%REPORT%" 2>&1
echo NPM_INSTALL_EXIT=%ERRORLEVEL% >> "%REPORT%"

echo --- NPM BUILD --- >> "%REPORT%"
call npm run build >> "%REPORT%" 2>&1
if errorlevel 1 (
  echo NPM_BUILD=failure >> "%REPORT%"
  copy /Y "%REPORT%" "%MAIN%\build_report.txt" >nul 2>&1
  exit /b 1
)
echo NPM_BUILD=success >> "%REPORT%"

cd /d "%BUILD_ROOT%"
if exist cmd\server\webdist rmdir /s /q cmd\server\webdist
mkdir cmd\server\webdist
xcopy /E /Y /I web\dist\* cmd\server\webdist\ >> "%REPORT%" 2>&1
echo COPY_WEBDIST_IN_TEMP=success >> "%REPORT%"

echo --- GO BUILD --- >> "%REPORT%"
go build -o k8man.exe ./cmd/server >> "%REPORT%" 2>&1
if errorlevel 1 (
  echo GO_BUILD=failure >> "%REPORT%"
) else (
  echo GO_BUILD=success >> "%REPORT%"
  echo BINARY=%BUILD_ROOT%\k8man.exe >> "%REPORT%"
)

echo NOTE=webdist NOT copied back to git tree >> "%REPORT%"
echo PIPELINE_DONE >> "%REPORT%"
echo === END %DATE% %TIME% >> "%REPORT%"
copy /Y "%REPORT%" "%MAIN%\build_report.txt" >nul 2>&1
endlocal

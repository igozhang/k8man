@echo off
setlocal EnableDelayedExpansion
set GOPROXY=https://goproxy.cn,direct
set GOSUMDB=sum.golang.google.cn
set MAIN=e:\gitee\k8man
set WT=e:\gitee\k8man-build-wt
set BUILD_ROOT=%MAIN%
set USE_WT=0

cd /d %MAIN%

REM Try isolated worktree
if not exist "%WT%\.git" (
  git worktree add -b build/isolated-auto "%WT%" HEAD >> "%MAIN%\build_report.txt" 2>&1
)
if exist "%WT%\.git" (
  set BUILD_ROOT=%WT%
  set USE_WT=1
  copy /Y "%MAIN%\go.mod" "%WT%\go.mod" >nul 2>&1
  copy /Y "%MAIN%\go.sum" "%WT%\go.sum" >nul 2>&1
  xcopy /E /Y /I /Q "%MAIN%\web\src" "%WT%\web\src\" >nul 2>&1
  xcopy /E /Y /I /Q "%MAIN%\web\package.json" "%WT%\web\" >nul 2>&1
  if exist "%MAIN%\web\package-lock.json" copy /Y "%MAIN%\web\package-lock.json" "%WT%\web\" >nul 2>&1
  if exist "%MAIN%\web\vite.config.js" copy /Y "%MAIN%\web\vite.config.js" "%WT%\web\" >nul 2>&1
  if exist "%MAIN%\web\index.html" copy /Y "%MAIN%\web\index.html" "%WT%\web\" >nul 2>&1
  xcopy /E /Y /I /Q "%MAIN%\cmd" "%WT%\cmd\" >nul 2>&1
  xcopy /E /Y /I /Q "%MAIN%\internal" "%WT%\internal\" >nul 2>&1
)

cd /d %BUILD_ROOT%
echo === BUILD START %DATE% %TIME% > build_report.txt
echo BUILD_ROOT=%BUILD_ROOT% >> build_report.txt
echo USE_WORKTREE=%USE_WT% >> build_report.txt
echo GOPROXY=%GOPROXY% >> build_report.txt
echo GOSUMDB=%GOSUMDB% >> build_report.txt

echo --- GO BUILD 1 --- >> build_report.txt
go build -o k8man.exe ./cmd/server >> build_report.txt 2>&1
if errorlevel 1 (
  echo GO_BUILD1=failure >> build_report.txt
) else (
  echo GO_BUILD1=success >> build_report.txt
)

cd /d %BUILD_ROOT%\web
echo --- NPM INSTALL --- >> %BUILD_ROOT%\build_report.txt
call npm install >> %BUILD_ROOT%\build_report.txt 2>&1
echo NPM_INSTALL_EXIT=%ERRORLEVEL% >> %BUILD_ROOT%\build_report.txt

echo --- NPM BUILD --- >> %BUILD_ROOT%\build_report.txt
call npm run build >> %BUILD_ROOT%\build_report.txt 2>&1
if errorlevel 1 (
  echo NPM_BUILD=failure >> %BUILD_ROOT%\build_report.txt
) else (
  echo NPM_BUILD=success >> %BUILD_ROOT%\build_report.txt
  cd /d %BUILD_ROOT%
  if not exist cmd\server\webdist mkdir cmd\server\webdist
  xcopy /E /Y /I web\dist\* cmd\server\webdist\ >> build_report.txt 2>&1
  echo COPY=success >> build_report.txt
)

cd /d %BUILD_ROOT%
echo --- GO BUILD 2 --- >> build_report.txt
go build -o k8man.exe ./cmd/server >> build_report.txt 2>&1
if errorlevel 1 (
  echo GO_BUILD2=failure >> build_report.txt
) else (
  echo GO_BUILD2=success >> build_report.txt
)

if exist k8man.exe (
  echo k8man.exe exists: True >> build_report.txt
  for %%A in (k8man.exe) do echo k8man.exe size=%%~zA >> build_report.txt
) else (
  echo k8man.exe exists: False >> build_report.txt
)

if exist web\dist (
  echo --- web/dist files --- >> build_report.txt
  dir /s /b web\dist >> build_report.txt 2>&1
)
if exist cmd\server\webdist (
  echo --- cmd/server/webdist files --- >> build_report.txt
  dir /s /b cmd\server\webdist >> build_report.txt 2>&1
)

REM Copy artifacts back to main if worktree
if "%USE_WT%"=="1" (
  echo --- COPY BACK TO MAIN --- >> build_report.txt
  if exist k8man.exe copy /Y k8man.exe "%MAIN%\k8man.exe" >> build_report.txt 2>&1
  if exist web\dist (
    if not exist "%MAIN%\web\dist" mkdir "%MAIN%\web\dist"
    xcopy /E /Y /I web\dist\* "%MAIN%\web\dist\" >> build_report.txt 2>&1
  )
  if exist cmd\server\webdist (
    if not exist "%MAIN%\cmd\server\webdist" mkdir "%MAIN%\cmd\server\webdist"
    xcopy /E /Y /I cmd\server\webdist\* "%MAIN%\cmd\server\webdist\" >> build_report.txt 2>&1
  )
  if exist go.sum copy /Y go.sum "%MAIN%\go.sum" >> build_report.txt 2>&1
  copy /Y build_report.txt "%MAIN%\build_report.txt" >nul 2>&1
  echo COPY_BACK=done >> build_report.txt
  echo COPY_BACK=done >> "%MAIN%\build_report.txt"
)

echo PIPELINE_DONE >> build_report.txt
echo === END %DATE% %TIME% >> build_report.txt
if not "%BUILD_ROOT%"=="%MAIN%" copy /Y build_report.txt "%MAIN%\build_report.txt" >nul 2>&1
endlocal

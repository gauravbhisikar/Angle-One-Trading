@echo off
setlocal

set API_ADDR=:9080
set SQLITE_PATH=trading.db
set STARTING_CAPITAL=100000

echo Building engine...
pushd "%~dp0engine"
go build -o engine.exe .\cmd\engine
if errorlevel 1 (
    echo Build failed. See errors above.
    pause
    exit /b 1
)
popd

echo Building contextbuilder-server...
pushd "%~dp0contextbuilder"
go build -o contextbuilder-server.exe .\cmd\server
if errorlevel 1 (
    echo Build failed. See errors above.
    pause
    exit /b 1
)
popd

start "contextbuilder-server" /min "%~dp0contextbuilder\contextbuilder-server.exe"

if exist "%~dp0agent\venv\Scripts\python.exe" (
    start "agent" /min "%~dp0agent\venv\Scripts\python.exe" "%~dp0agent\api.py"
) else (
    echo Skipping agent — no venv found at agent\venv. See agent\README.md to set it up.
)

start "" /min powershell -NoProfile -Command "Start-Sleep -Seconds 2; Start-Process 'http://localhost:9080/'"

echo.
echo Starting AI Trading Engine on http://localhost:9080
echo (contextbuilder-server on :9090, agent on :9091 in separate minimized windows)
echo Close this window (or Ctrl+C) to stop the engine. Close the other windows to stop them.
echo.
cd /d "%~dp0engine"
engine.exe

endlocal

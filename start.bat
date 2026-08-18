@echo off
setlocal
cd /d "%~dp0"

where python >nul 2>nul
if errorlevel 1 (
    echo Python not found on PATH. Install Python 3 and re-run.
    pause
    exit /b 1
)

echo Starting Pre-Market Dashboard on http://localhost:9080 ...
start "Pre-Market Dashboard" python app.py

start "" /min powershell -NoProfile -Command "Start-Sleep -Seconds 4; Start-Process 'http://localhost:9080/'"

echo.
echo Dashboard: http://localhost:9080
echo (Live refresh is OFF by default - flip the Live switch in the top bar to enable auto-refresh.)
echo Close the "Pre-Market Dashboard" window to stop it.
echo.

endlocal
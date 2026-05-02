@echo off
setlocal enabledelayedexpansion
cd /d "%~dp0"

:: ==========================================
:: TeleCloud Auto-Installer for Windows (EN)
:: ==========================================

:: 1. Check for Admin rights
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo [!] This script requires Administrator privileges.
    echo [+] Automatically requesting Admin rights...
    powershell -Command "Start-Process -FilePath '%0' -Verb RunAs"
    exit /b
)

set "BASE_DIR=%CD%"
set "BIN_NAME=telecloud.exe"
set "REPO=dabeecao/telecloud-go"

:MENU
cls
echo ==========================================
echo       TeleCloud Management Menu (Windows)
echo ==========================================
echo 1. Install / Update TeleCloud
echo 2. Setup Cloudflare Tunnel
echo 3. Authenticate with Telegram (First Time)
echo 4. Start TeleCloud (Background)
echo 5. Stop TeleCloud
echo 6. View Logs
echo 7. Edit .env
echo 8. Exit
echo ==========================================
set /p choice="Select an option (1-8): "

if "%choice%"=="1" goto INSTALL
if "%choice%"=="2" goto CLOUDFLARED_SETUP
if "%choice%"=="3" goto AUTH
if "%choice%"=="4" goto START_APP
if "%choice%"=="5" goto STOP_APP
if "%choice%"=="6" goto VIEW_LOGS
if "%choice%"=="7" goto EDIT_ENV
if "%choice%"=="8" exit /b
goto MENU

:INSTALL
echo [+] Checking for FFmpeg...
where ffmpeg >nul 2>nul
if !errorlevel! equ 0 (
    echo [v] FFmpeg is already installed on the system.
    goto DOWNLOAD_APP
)

if exist "ffmpeg.exe" (
    echo [v] Found ffmpeg.exe in current directory.
    goto DOWNLOAD_APP
)

echo [!] FFmpeg not found. Attempting to install...
where winget >nul 2>nul
if !errorlevel! equ 0 (
    echo [+] Installing via winget...
    winget install ffmpeg --source winget
    if !errorlevel! equ 0 goto DOWNLOAD_APP
)

echo [!] winget not found or installation failed. Downloading portable version via PowerShell...
powershell -Command "$progressPreference = 'SilentlyContinue'; Invoke-WebRequest -Uri 'https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip' -OutFile 'ffmpeg.zip'"
if not exist "ffmpeg.zip" (
    echo [!] Could not download FFmpeg. Please install manually.
    pause
    goto MENU
)

echo [+] Extracting FFmpeg...
powershell -Command "Expand-Archive -Path 'ffmpeg.zip' -DestinationPath 'ffmpeg_temp' -Force"
for /r "ffmpeg_temp" %%i in (ffmpeg.exe) do move /y "%%i" . >nul
del ffmpeg.zip
rd /s /q ffmpeg_temp

if exist "ffmpeg.exe" (
    echo [v] Downloaded ffmpeg.exe successfully.
) else (
    echo [!] Extraction failed or ffmpeg.exe not found.
    pause
)

:DOWNLOAD_APP
echo [+] Fetching latest version from GitHub...
for /f "tokens=*" %%a in ('powershell -Command "$r = Invoke-RestMethod -Uri 'https://api.github.com/repos/%REPO%/releases/latest'; $r.assets | Where-Object { $_.name -like '*windows_amd64.zip*' } | Select-Object -ExpandProperty browser_download_url"') do set "DL_URL=%%a"

if "%DL_URL%"=="" (
    echo [!] Could not find latest release for Windows.
    pause
    goto MENU
)

echo [+] Downloading latest version...
powershell -Command "Invoke-WebRequest -Uri '%DL_URL%' -OutFile 'telecloud.zip'"
if %errorlevel% neq 0 (
    echo [!] Download failed.
    pause
    goto MENU
)

echo [+] Extracting...
powershell -Command "Expand-Archive -Path 'telecloud.zip' -DestinationPath '.' -Force"
del telecloud.zip

if not exist ".env" (
    echo [+] Creating .env from example...
    if exist "env.example" (
        copy env.example .env
    ) else (
        powershell -Command "Invoke-WebRequest -Uri 'https://raw.githubusercontent.com/%REPO%/main/env.example' -OutFile '.env'"
    )
    echo [!] Please edit .env with your credentials!
    pause
    notepad .env
)

echo [v] Installation/Update complete!
pause
goto MENU

:CLOUDFLARED_SETUP
echo [+] Checking for Cloudflared...
where cloudflared >nul 2>nul
if !errorlevel! equ 0 (
    echo [v] Cloudflared is already installed on the system.
    goto CF_LOGIN
)

if exist "cloudflared.exe" (
    echo [v] Found cloudflared.exe in current directory.
    goto CF_LOGIN
)

echo [!] Cloudflared not found. Attempting to install...
where winget >nul 2>nul
if !errorlevel! equ 0 (
    echo [+] Installing via winget...
    winget install Cloudflare.cloudflared
    if !errorlevel! equ 0 goto CF_LOGIN
)

echo [!] winget not found or installation failed. Downloading cloudflared.exe directly...
powershell -Command "$progressPreference = 'SilentlyContinue'; Invoke-WebRequest -Uri 'https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe' -OutFile 'cloudflared.exe'"

if exist "cloudflared.exe" (
    echo [v] Downloaded cloudflared.exe successfully.
) else (
    echo [!] Could not download cloudflared.exe.
    pause
    goto MENU
)

:CF_LOGIN
echo [+] Logging into Cloudflare...
cloudflared tunnel login

echo [+] Creating tunnel 'telecloud-tunnel'...
cloudflared tunnel create telecloud-tunnel
if %errorlevel% neq 0 (
    echo [!] Tunnel creation skipped (it may already exist).
)

set /p domain="Enter your domain (e.g. tele.yourdomain.com): "
if not "%domain%"=="" (
    echo [+] Setting up DNS route...
    cloudflared tunnel route dns telecloud-tunnel %domain%
    echo %domain% > domain.txt
)

echo [v] Cloudflare Tunnel setup finished.
pause
goto MENU

:AUTH
echo [+] Starting authentication flow...
if not exist "%BIN_NAME%" (
    echo [!] %BIN_NAME% not found. Please install first.
    pause
    goto MENU
)
"%BIN_NAME%" -auth
pause
goto MENU

:START_APP
echo [+] Starting TeleCloud in background...
if not exist "%BIN_NAME%" (
    echo [!] %BIN_NAME% not found. Please install first.
    pause
    goto MENU
)
:: Redirect stdout and stderr to app.log via cmd wrapper
powershell -Command "Start-Process -FilePath 'cmd.exe' -ArgumentList '/c %BIN_NAME% >> app.log 2>&1' -WindowStyle Hidden"

if exist "domain.txt" (
    for /f "usebackq tokens=*" %%a in ("domain.txt") do set "MY_DOMAIN=%%a"
    if not "!MY_DOMAIN!"=="" (
        set "APP_PORT=8091"
        for /f "tokens=2 delims==" %%i in ('findstr /R "^PORT=" .env 2^>nul') do (
            set "TMP_PORT=%%i"
            set "TMP_PORT=!TMP_PORT: =!"
            if not "!TMP_PORT!"=="" set "APP_PORT=!TMP_PORT!"
        )
        echo [+] Starting Cloudflare Tunnel for !MY_DOMAIN! on port !APP_PORT!...
        powershell -Command "Start-Process -FilePath 'cmd.exe' -ArgumentList '/c cloudflared tunnel run --url http://localhost:!APP_PORT! telecloud-tunnel >> tunnel.log 2>&1' -WindowStyle Hidden"
    )
)

echo [v] App started in background. Logs are being written to app.log.
pause
goto MENU

:STOP_APP
echo [+] Stopping TeleCloud processes...
taskkill /f /im "%BIN_NAME%" >nul 2>nul
taskkill /f /im cloudflared.exe >nul 2>nul
echo [v] App stopped (if it was running).
pause
goto MENU

:VIEW_LOGS
cls
echo ==========================================
echo 1. View App Logs (Telecloud)
echo 2. View Tunnel Logs (Cloudflared)
echo 3. Back
echo ==========================================
set /p log_choice="Select log to view (1-3): "
if "%log_choice%"=="1" (
    if exist "app.log" (
        echo [!] Press Ctrl+C to exit log view...
        powershell -Command "Get-Content app.log -Tail 50 -Wait"
    ) else (
        echo [!] app.log not found.
        pause
    )
)
if "%log_choice%"=="2" (
    if exist "tunnel.log" (
        echo [!] Press Ctrl+C to exit log view...
        powershell -Command "Get-Content tunnel.log -Tail 50 -Wait"
    ) else (
        echo [!] tunnel.log not found.
        pause
    )
)
goto MENU

:EDIT_ENV
notepad .env
goto MENU

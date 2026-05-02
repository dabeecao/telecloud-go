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
echo 2. Manage Cloudflare Tunnel
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
cls
echo ==========================================
echo     Cloudflare Tunnel Manager
echo ==========================================
echo 1. Setup / Update tunnel
echo 2. View tunnel status
echo 3. Change domain
echo 4. Delete tunnel
echo 5. Back
echo ==========================================
set /p cf_choice="Select an option (1-5): "

if "!cf_choice!"=="1" goto CF_DO_SETUP
if "!cf_choice!"=="2" goto CF_STATUS
if "!cf_choice!"=="3" goto CF_CHANGE_DOMAIN
if "!cf_choice!"=="4" goto CF_DELETE
if "!cf_choice!"=="5" goto MENU
goto CLOUDFLARED_SETUP

:: -------------------------------------------------------
:CF_DO_SETUP
echo [+] Checking for Cloudflared...

set "CF_EXE="
if exist "cloudflared.exe" (
    set "CF_EXE=%CD%\cloudflared.exe"
    echo [v] Found cloudflared.exe in current directory.
    goto CF_LOGIN
)

:: Refresh PATH to detect winget-installed cloudflared
for /f "tokens=*" %%p in ('powershell -NoProfile -Command "[System.Environment]::GetEnvironmentVariable('PATH','Machine')"') do set "PATH=%%p;%PATH%"
where cloudflared >nul 2>nul
if !errorlevel! equ 0 (
    echo [v] Cloudflared is already installed on the system.
    set "CF_EXE=cloudflared"
    goto CF_LOGIN
)

echo [!] Cloudflared not found. Attempting to install...
where winget >nul 2>nul
if !errorlevel! equ 0 (
    echo [+] Installing via winget...
    winget install Cloudflare.cloudflared
    for /f "tokens=*" %%p in ('powershell -NoProfile -Command "[System.Environment]::GetEnvironmentVariable('PATH','Machine')"') do set "PATH=%%p;%PATH%"
    where cloudflared >nul 2>nul
    if !errorlevel! equ 0 (
        set "CF_EXE=cloudflared"
        goto CF_LOGIN
    )
)

echo [!] winget not available. Downloading cloudflared.exe directly...
powershell -Command "$progressPreference = 'SilentlyContinue'; Invoke-WebRequest -Uri 'https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe' -OutFile 'cloudflared.exe'"

if exist "cloudflared.exe" (
    echo [v] Downloaded cloudflared.exe successfully.
    set "CF_EXE=%CD%\cloudflared.exe"
) else (
    echo [!] Could not download cloudflared.exe.
    pause
    goto CLOUDFLARED_SETUP
)

:CF_LOGIN
if "!CF_EXE!"=="" (
    if exist "cloudflared.exe" (
        set "CF_EXE=%CD%\cloudflared.exe"
    ) else (
        set "CF_EXE=cloudflared"
    )
)

:: Skip login if already authenticated (cert.pem exists)
if exist "%USERPROFILE%\.cloudflared\cert.pem" (
    echo [v] Already logged into Cloudflare, skipping login.
    goto CF_CREATE_TUNNEL
)

echo [+] Opening browser to log into Cloudflare...
"!CF_EXE!" tunnel login
if !errorlevel! neq 0 (
    echo [!] Cloudflare login failed. Please try again.
    pause
    goto CLOUDFLARED_SETUP
)

:CF_CREATE_TUNNEL
:: Load saved tunnel name or generate a new random one
set "TUNNEL_NAME="
if exist "tunnel-name.txt" (
    for /f "usebackq tokens=*" %%a in ("tunnel-name.txt") do set "TUNNEL_NAME=%%a"
)
if "!TUNNEL_NAME!"=="" (
    for /f "tokens=*" %%r in ('powershell -NoProfile -Command "-join ('abcdefghijklmnopqrstuvwxyz0123456789'.ToCharArray() | Get-Random -Count 6)"') do set "RAND_SUFFIX=%%r"
    set "TUNNEL_NAME=telecloud-!RAND_SUFFIX!"
    echo !TUNNEL_NAME! > tunnel-name.txt
    echo [+] New tunnel name: !TUNNEL_NAME!
)

:: Check if tunnel already exists
"!CF_EXE!" tunnel info !TUNNEL_NAME! >nul 2>nul
if !errorlevel! equ 0 (
    echo [v] Tunnel '!TUNNEL_NAME!' already exists, skipping creation.
    goto CF_DOMAIN
)

echo [+] Creating tunnel '!TUNNEL_NAME!'...
"!CF_EXE!" tunnel create !TUNNEL_NAME!
if !errorlevel! neq 0 (
    echo [!] Tunnel creation failed. Please check and try again.
    pause
    goto CLOUDFLARED_SETUP
)

:CF_DOMAIN
set /p domain="Enter your domain (e.g. tele.yourdomain.com): "
if not "!domain!"=="" (
    echo [+] Setting up DNS route...
    "!CF_EXE!" tunnel route dns -f !TUNNEL_NAME! !domain!
    echo !domain! > domain.txt
)

echo [v] Cloudflare Tunnel setup complete.
pause
goto CLOUDFLARED_SETUP

:: -------------------------------------------------------
:CF_STATUS
cls
echo [+] Fetching tunnel info...
if exist "cloudflared.exe" ( set "CF_EXE=%CD%\cloudflared.exe" ) else ( set "CF_EXE=cloudflared" )
set "TUNNEL_NAME=telecloud"
if exist "tunnel-name.txt" (
    for /f "usebackq tokens=*" %%a in ("tunnel-name.txt") do set "TUNNEL_NAME=%%a"
)
echo [+] Tunnel name: !TUNNEL_NAME!
"!CF_EXE!" tunnel info !TUNNEL_NAME!
if !errorlevel! neq 0 (
    echo [!] Tunnel '!TUNNEL_NAME!' not found. It may not have been created yet.
)
if exist "domain.txt" (
    for /f "usebackq tokens=*" %%a in ("domain.txt") do echo [+] Current domain: %%a
)
pause
goto CLOUDFLARED_SETUP

:: -------------------------------------------------------
:CF_CHANGE_DOMAIN
if exist "cloudflared.exe" ( set "CF_EXE=%CD%\cloudflared.exe" ) else ( set "CF_EXE=cloudflared" )
set "TUNNEL_NAME=telecloud"
if exist "tunnel-name.txt" (
    for /f "usebackq tokens=*" %%a in ("tunnel-name.txt") do set "TUNNEL_NAME=%%a"
)
set /p domain="Enter new domain (e.g. tele.yourdomain.com): "
if "!domain!"=="" (
    echo [!] Domain cannot be empty.
    pause
    goto CLOUDFLARED_SETUP
)
echo [+] Updating DNS route for '!TUNNEL_NAME!'...
"!CF_EXE!" tunnel route dns -f !TUNNEL_NAME! !domain!
echo !domain! > domain.txt
echo [v] Domain updated to: !domain!
pause
goto CLOUDFLARED_SETUP

:: -------------------------------------------------------
:CF_DELETE
set "TUNNEL_NAME=telecloud"
if exist "tunnel-name.txt" (
    for /f "usebackq tokens=*" %%a in ("tunnel-name.txt") do set "TUNNEL_NAME=%%a"
)
echo [!] WARNING: This will permanently delete tunnel '!TUNNEL_NAME!' from Cloudflare!
set /p confirm_del="Type YES to confirm deletion: "
if /i not "!confirm_del!"=="YES" (
    echo [x] Cancelled.
    pause
    goto CLOUDFLARED_SETUP
)
if exist "cloudflared.exe" ( set "CF_EXE=%CD%\cloudflared.exe" ) else ( set "CF_EXE=cloudflared" )
echo [+] Removing DNS route...
"!CF_EXE!" tunnel route dns --overwrite-dns !TUNNEL_NAME! >nul 2>nul
echo [+] Deleting tunnel...
"!CF_EXE!" tunnel delete -f !TUNNEL_NAME!
if !errorlevel! equ 0 (
    echo [v] Tunnel deleted successfully.
    if exist "domain.txt" del domain.txt
    if exist "tunnel-name.txt" del tunnel-name.txt
) else (
    echo [!] Tunnel deletion failed. Please check and try again.
)
pause
goto CLOUDFLARED_SETUP

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
        set "TUNNEL_NAME=telecloud"
        if exist "tunnel-name.txt" (
            for /f "usebackq tokens=*" %%t in ("tunnel-name.txt") do set "TUNNEL_NAME=%%t"
        )
        set "APP_PORT=8091"
        for /f "tokens=2 delims==" %%i in ('findstr /R "^PORT=" .env 2^>nul') do (
            set "TMP_PORT=%%i"
            set "TMP_PORT=!TMP_PORT: =!"
            if not "!TMP_PORT!"=="" set "APP_PORT=!TMP_PORT!"
        )
        echo [+] Starting Cloudflare Tunnel '!TUNNEL_NAME!' for !MY_DOMAIN! on port !APP_PORT!...
        powershell -Command "Start-Process -FilePath 'cmd.exe' -ArgumentList '/c cloudflared tunnel run --url http://localhost:!APP_PORT! !TUNNEL_NAME! >> tunnel.log 2>&1' -WindowStyle Hidden"
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
echo 1. View App Logs (TeleCloud)
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

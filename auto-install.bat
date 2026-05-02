@echo off
setlocal enabledelayedexpansion
cd /d "%~dp0"

:: ==========================================
:: TeleCloud Auto-Installer for Windows (VN)
:: ==========================================

:: 1. Kiểm tra quyền Admin
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo [!] Script nay can chay voi quyen Administrator.
    echo [+] Dang tu dong yeu cau quyen Admin...
    powershell -Command "Start-Process -FilePath '%0' -Verb RunAs"
    exit /b
)

set "BASE_DIR=%CD%"
set "BIN_NAME=telecloud.exe"
set "REPO=dabeecao/telecloud-go"

:MENU
cls
echo ==========================================
echo       Menu Quan Ly TeleCloud (Windows)
echo ==========================================
echo 1. Cai dat / Cap nhat TeleCloud
echo 2. Thiet lap Cloudflare Tunnel
echo 3. Dang nhap Telegram (Lan dau)
echo 4. Khoi dong TeleCloud (Chay ngam)
echo 5. Dung TeleCloud
echo 6. Xem Nhat ky (Logs)
echo 7. Chinh sua .env
echo 8. Thoat
echo ==========================================
set /p choice="Chon mot tuy chon (1-8): "

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
echo [+] Dang kiem tra FFmpeg...
where ffmpeg >nul 2>nul
if !errorlevel! equ 0 (
    echo [v] FFmpeg da duoc cai dat tren he thong.
    goto DOWNLOAD_APP
)

if exist "ffmpeg.exe" (
    echo [v] Tim thay ffmpeg.exe trong thu muc hien tai.
    goto DOWNLOAD_APP
)

echo [!] Khong tim thay FFmpeg. Dang thu cai dat...
where winget >nul 2>nul
if !errorlevel! equ 0 (
    echo [+] Dang cai dat qua winget...
    winget install ffmpeg --source winget
    if !errorlevel! equ 0 goto DOWNLOAD_APP
)

echo [!] Khong co winget hoac cai dat that bai. Dang tai ban portable qua PowerShell...
powershell -Command "$progressPreference = 'SilentlyContinue'; Invoke-WebRequest -Uri 'https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip' -OutFile 'ffmpeg.zip'"
if not exist "ffmpeg.zip" (
    echo [!] Khong the tai FFmpeg. Vui long cai dat thu cong.
    pause
    goto MENU
)

echo [+] Dang giai nen FFmpeg...
powershell -Command "Expand-Archive -Path 'ffmpeg.zip' -DestinationPath 'ffmpeg_temp' -Force"
for /r "ffmpeg_temp" %%i in (ffmpeg.exe) do move /y "%%i" . >nul
del ffmpeg.zip
rd /s /q ffmpeg_temp

if exist "ffmpeg.exe" (
    echo [v] Da tai xong ffmpeg.exe.
) else (
    echo [!] Giai nen that bai hoac khong tim thay ffmpeg.exe.
    pause
)

:DOWNLOAD_APP
echo [+] Dang lay thong tin phien ban moi nhat tu GitHub...
for /f "tokens=*" %%a in ('powershell -Command "$r = Invoke-RestMethod -Uri 'https://api.github.com/repos/%REPO%/releases/latest'; $r.assets | Where-Object { $_.name -like '*windows_amd64.zip*' } | Select-Object -ExpandProperty browser_download_url"') do set "DL_URL=%%a"

if "%DL_URL%"=="" (
    echo [!] Khong tim thay ban phat hanh cho Windows.
    pause
    goto MENU
)

echo [+] Dang tai phien ban moi nhat...
powershell -Command "Invoke-WebRequest -Uri '%DL_URL%' -OutFile 'telecloud.zip'"
if %errorlevel% neq 0 (
    echo [!] Tai xuong that bai.
    pause
    goto MENU
)

echo [+] Dang giai nen...
powershell -Command "Expand-Archive -Path 'telecloud.zip' -DestinationPath '.' -Force"
del telecloud.zip

if not exist ".env" (
    echo [+] Dang tao file .env tu file mau...
    if exist "env.example" (
        copy env.example .env
    ) else (
        powershell -Command "Invoke-WebRequest -Uri 'https://raw.githubusercontent.com/%REPO%/main/env.example' -OutFile '.env'"
    )
    echo [!] Vui long chinh sua .env voi thong tin cua ban!
    pause
    notepad .env
)

echo [v] Cai dat/Cap nhat hoan tat!
pause
goto MENU

:CLOUDFLARED_SETUP
echo [+] Dang kiem tra Cloudflared...
where cloudflared >nul 2>nul
if !errorlevel! equ 0 (
    echo [v] Cloudflared da duoc cai dat tren he thong.
    goto CF_LOGIN
)

if exist "cloudflared.exe" (
    echo [v] Tim thay cloudflared.exe trong thu muc hien tai.
    goto CF_LOGIN
)

echo [!] Khong tim thay Cloudflared. Dang thu cai dat...
where winget >nul 2>nul
if !errorlevel! equ 0 (
    echo [+] Dang cai dat qua winget...
    winget install Cloudflare.cloudflared
    if !errorlevel! equ 0 goto CF_LOGIN
)

echo [!] Khong co winget hoac cai dat that bai. Dang tai cloudflared.exe truc tiep...
powershell -Command "$progressPreference = 'SilentlyContinue'; Invoke-WebRequest -Uri 'https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe' -OutFile 'cloudflared.exe'"

if exist "cloudflared.exe" (
    echo [v] Da tai xong cloudflared.exe.
) else (
    echo [!] Khong the tai cloudflared.exe.
    pause
    goto MENU
)

:CF_LOGIN
echo [+] Dang dang nhap vao Cloudflare...
cloudflared tunnel login

echo [+] Dang tao tunnel 'telecloud-tunnel'...
cloudflared tunnel create telecloud-tunnel
if %errorlevel% neq 0 (
    echo [!] Bo qua buoc tao tunnel (co the da ton tai).
)

set /p domain="Nhap ten mien cua ban (VD: tele.domain.com): "
if not "%domain%"=="" (
    echo [+] Dang thiet lap DNS route...
    cloudflared tunnel route dns telecloud-tunnel %domain%
    echo %domain% > domain.txt
)

echo [v] Thiet lap Cloudflare Tunnel hoan tat.
pause
goto MENU

:AUTH
echo [+] Dang bat dau xac thuc...
if not exist "%BIN_NAME%" (
    echo [!] Khong tim thay %BIN_NAME%. Vui long cai dat truoc.
    pause
    goto MENU
)
"%BIN_NAME%" -auth
pause
goto MENU

:START_APP
echo [+] Dang khoi dong TeleCloud chay ngam...
if not exist "%BIN_NAME%" (
    echo [!] Khong tim thay %BIN_NAME%. Vui long cai dat truoc.
    pause
    goto MENU
)
:: Gộp stdout và stderr vào app.log thông qua cmd wrapper
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
        echo [+] Dang khoi dong Cloudflare Tunnel cho !MY_DOMAIN! tai cong !APP_PORT!...
        powershell -Command "Start-Process -FilePath 'cmd.exe' -ArgumentList '/c cloudflared tunnel run --url http://localhost:!APP_PORT! telecloud-tunnel >> tunnel.log 2>&1' -WindowStyle Hidden"
    )
)

echo [v] Ung dung da duoc khoi chay ngam. Nhat ky duoc ghi vao app.log.
pause
goto MENU

:STOP_APP
echo [+] Dang dung cac tien trinh TeleCloud...
taskkill /f /im "%BIN_NAME%" >nul 2>nul
taskkill /f /im cloudflared.exe >nul 2>nul
echo [v] Da dung ung dung (neu dang chay).
pause
goto MENU

:VIEW_LOGS
cls
echo ==========================================
echo 1. Xem Nhat ky Ung dung (Telecloud)
echo 2. Xem Nhat ky Cloudflare Tunnel
echo 3. Quay lai
echo ==========================================
set /p log_choice="Chon nhat ky muon xem (1-3): "
if "%log_choice%"=="1" (
    if exist "app.log" (
        echo [!] Nhan Ctrl+C de thoat xem log...
        powershell -Command "Get-Content app.log -Tail 50 -Wait"
    ) else (
        echo [!] Khong tim thay app.log.
        pause
    )
)
if "%log_choice%"=="2" (
    if exist "tunnel.log" (
        echo [!] Nhan Ctrl+C de thoat xem log...
        powershell -Command "Get-Content tunnel.log -Tail 50 -Wait"
    ) else (
        echo [!] Khong tim thay tunnel.log.
        pause
    )
)
goto MENU

:EDIT_ENV
notepad .env
goto MENU

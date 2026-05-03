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

echo [+] Dang kiem tra yt-dlp...
where yt-dlp >nul 2>nul
if !errorlevel! equ 0 (
    echo [v] yt-dlp da duoc cai dat tren he thong.
    goto DOWNLOAD_APP
)
if exist "yt-dlp.exe" (
    echo [v] Tim thay yt-dlp.exe trong thu muc hien tai.
    goto DOWNLOAD_APP
)

set /p install_ytdlp="[?] Ban co muon cai dat yt-dlp (Tai video/audio tu URL) khong? (y/n): "
if /i not "!install_ytdlp!"=="y" goto DOWNLOAD_APP

echo [+] Dang tai yt-dlp.exe...
powershell -Command "$progressPreference = 'SilentlyContinue'; Invoke-WebRequest -Uri 'https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe' -OutFile 'yt-dlp.exe'"
if exist "yt-dlp.exe" (
    echo [v] Da tai xong yt-dlp.exe.
) else (
    echo [!] Tai yt-dlp.exe that bai.
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

    :: Tu dong dien duong dan neu ton tai
    if exist "ffmpeg.exe" (
        powershell -Command "(Get-Content .env) -replace '^FFMPEG_PATH=.*', 'FFMPEG_PATH=ffmpeg' | Set-Content .env"
    )
    if exist "yt-dlp.exe" (
        powershell -Command "(Get-Content .env) -replace '^#?YTDLP_PATH=.*', 'YTDLP_PATH=yt-dlp' | Set-Content .env"
    )

    echo [!] Vui long chinh sua .env voi thong tin cua ban!
    pause
    notepad .env
)

echo [v] Cai dat/Cap nhat hoan tat!
pause
goto MENU

:CLOUDFLARED_SETUP
cls
echo ==========================================
echo     Quan Ly Cloudflare Tunnel
echo ==========================================
echo 1. Thiet lap / Cap nhat tunnel
echo 2. Xem trang thai tunnel
echo 3. Thay doi ten mien
echo 4. Xoa tunnel
echo 5. Quay lai
echo ==========================================
set /p cf_choice="Chon tuy chon (1-5): "

if "!cf_choice!"=="1" goto CF_DO_SETUP
if "!cf_choice!"=="2" goto CF_STATUS
if "!cf_choice!"=="3" goto CF_CHANGE_DOMAIN
if "!cf_choice!"=="4" goto CF_DELETE
if "!cf_choice!"=="5" goto MENU
goto CLOUDFLARED_SETUP

:: -------------------------------------------------------
:CF_DO_SETUP
echo [+] Dang kiem tra Cloudflared...

set "CF_EXE="
if exist "cloudflared.exe" (
    set "CF_EXE=%CD%\cloudflared.exe"
    echo [v] Tim thay cloudflared.exe trong thu muc hien tai.
    goto CF_LOGIN
)

:: Refresh PATH de phat hien cai dat moi (winget co the them vao PATH chua reload)
for /f "tokens=*" %%p in ('powershell -NoProfile -Command "[System.Environment]::GetEnvironmentVariable('PATH','Machine')"') do set "PATH=%%p;%PATH%"
where cloudflared >nul 2>nul
if !errorlevel! equ 0 (
    echo [v] Cloudflared da duoc cai dat tren he thong.
    set "CF_EXE=cloudflared"
    goto CF_LOGIN
)

echo [!] Khong tim thay Cloudflared. Dang thu cai dat...
where winget >nul 2>nul
if !errorlevel! equ 0 (
    echo [+] Dang cai dat qua winget...
    winget install Cloudflare.cloudflared
    for /f "tokens=*" %%p in ('powershell -NoProfile -Command "[System.Environment]::GetEnvironmentVariable('PATH','Machine')"') do set "PATH=%%p;%PATH%"
    where cloudflared >nul 2>nul
    if !errorlevel! equ 0 (
        set "CF_EXE=cloudflared"
        goto CF_LOGIN
    )
)

echo [!] Khong co winget. Dang tai cloudflared.exe truc tiep...
powershell -Command "$progressPreference = 'SilentlyContinue'; Invoke-WebRequest -Uri 'https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe' -OutFile 'cloudflared.exe'"

if exist "cloudflared.exe" (
    echo [v] Da tai xong cloudflared.exe.
    set "CF_EXE=%CD%\cloudflared.exe"
) else (
    echo [!] Khong the tai cloudflared.exe.
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

:: Kiem tra da dang nhap Cloudflare chua (cert.pem da ton tai)
if exist "%USERPROFILE%\.cloudflared\cert.pem" (
    echo [v] Da dang nhap Cloudflare truoc do, bo qua buoc login.
    goto CF_CREATE_TUNNEL
)

echo [+] Dang mo trinh duyet de dang nhap Cloudflare...
"!CF_EXE!" tunnel login
if !errorlevel! neq 0 (
    echo [!] Dang nhap Cloudflare that bai. Vui long thu lai.
    pause
    goto CLOUDFLARED_SETUP
)

:CF_CREATE_TUNNEL
:: Doc ten tunnel da luu, hoac sinh ten moi ngau nhien
set "TUNNEL_NAME="
if exist "tunnel-name.txt" (
    for /f "usebackq tokens=*" %%a in ("tunnel-name.txt") do set "TUNNEL_NAME=%%a"
)
if "!TUNNEL_NAME!"=="" (
    for /f "tokens=*" %%r in ('powershell -NoProfile -Command "-join ('abcdefghijklmnopqrstuvwxyz0123456789'.ToCharArray() | Get-Random -Count 6)"') do set "RAND_SUFFIX=%%r"
    set "TUNNEL_NAME=telecloud-!RAND_SUFFIX!"
    echo !TUNNEL_NAME! > tunnel-name.txt
    echo [+] Ten tunnel moi: !TUNNEL_NAME!
)

:: Kiem tra tunnel da ton tai chua
"!CF_EXE!" tunnel info !TUNNEL_NAME! >nul 2>nul
if !errorlevel! equ 0 (
    echo [v] Tunnel '!TUNNEL_NAME!' da ton tai, bo qua buoc tao.
    goto CF_DOMAIN
)

echo [+] Dang tao tunnel '!TUNNEL_NAME!'...
"!CF_EXE!" tunnel create !TUNNEL_NAME!
if !errorlevel! neq 0 (
    echo [!] Tao tunnel that bai. Vui long kiem tra lai.
    pause
    goto CLOUDFLARED_SETUP
)

:CF_DOMAIN
set /p domain="Nhap ten mien cua ban (VD: tele.domain.com): "
if not "!domain!"=="" (
    echo [+] Dang thiet lap DNS route...
    "!CF_EXE!" tunnel route dns -f !TUNNEL_NAME! !domain!
    echo !domain! > domain.txt
)

echo [v] Thiet lap Cloudflare Tunnel hoan tat.
pause
goto CLOUDFLARED_SETUP

:: -------------------------------------------------------
:CF_STATUS
cls
echo [+] Dang lay thong tin tunnel...
if exist "cloudflared.exe" ( set "CF_EXE=%CD%\cloudflared.exe" ) else ( set "CF_EXE=cloudflared" )
set "TUNNEL_NAME=telecloud"
if exist "tunnel-name.txt" (
    for /f "usebackq tokens=*" %%a in ("tunnel-name.txt") do set "TUNNEL_NAME=%%a"
)
echo [+] Ten tunnel: !TUNNEL_NAME!
"!CF_EXE!" tunnel info !TUNNEL_NAME!
if !errorlevel! neq 0 (
    echo [!] Khong tim thay tunnel '!TUNNEL_NAME!'. Co the chua duoc tao.
)
if exist "domain.txt" (
    for /f "usebackq tokens=*" %%a in ("domain.txt") do echo [+] Ten mien hien tai: %%a
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
set /p domain="Nhap ten mien moi (VD: tele.domain.com): "
if "!domain!"=="" (
    echo [!] Ten mien khong duoc de trong.
    pause
    goto CLOUDFLARED_SETUP
)
echo [+] Dang cap nhat DNS route cho '!TUNNEL_NAME!'...
"!CF_EXE!" tunnel route dns -f !TUNNEL_NAME! !domain!
echo !domain! > domain.txt
echo [v] Da cap nhat ten mien thanh: !domain!
pause
goto CLOUDFLARED_SETUP

:: -------------------------------------------------------
:CF_DELETE
set "TUNNEL_NAME=telecloud"
if exist "tunnel-name.txt" (
    for /f "usebackq tokens=*" %%a in ("tunnel-name.txt") do set "TUNNEL_NAME=%%a"
)
echo [!] CANH BAO: Thao tac nay se xoa tunnel '!TUNNEL_NAME!' khoi Cloudflare!
set /p confirm_del="Nhap YES de xac nhan xoa: "
if /i not "!confirm_del!"=="YES" (
    echo [x] Da huy.
    pause
    goto CLOUDFLARED_SETUP
)
if exist "cloudflared.exe" ( set "CF_EXE=%CD%\cloudflared.exe" ) else ( set "CF_EXE=cloudflared" )
echo [+] Dang xoa DNS route...
"!CF_EXE!" tunnel route dns --overwrite-dns !TUNNEL_NAME! >nul 2>nul
echo [+] Dang xoa tunnel...
"!CF_EXE!" tunnel delete -f !TUNNEL_NAME!
if !errorlevel! equ 0 (
    echo [v] Da xoa tunnel thanh cong.
    if exist "domain.txt" del domain.txt
    if exist "tunnel-name.txt" del tunnel-name.txt
) else (
    echo [!] Xoa tunnel that bai. Vui long kiem tra lai.
)
pause
goto CLOUDFLARED_SETUP

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

:: Xoa log cu de kiem tra log moi
type nul > app.log

:: Gộp stdout và stderr vào app.log thông qua cmd wrapper
powershell -Command "Start-Process -FilePath 'cmd.exe' -ArgumentList '/c %BIN_NAME% >> app.log 2>&1' -WindowStyle Hidden"

:: Kiem tra trang thai khoi dong
echo [+] Dang kiem tra trang thai khoi dong (cho toi da 30s)...
set /a timeout=30
:CHECK_LOOP
findstr /C:"Starting TeleCloud on port" app.log >nul
if !errorlevel! equ 0 (
    echo [v] TeleCloud da khoi dong thanh cong!
    goto START_TUNNEL
)
findstr /C:"TeleCloud shut down" app.log >nul
if !errorlevel! equ 0 (
    echo [!] TeleCloud khoi dong that bai. Vui long kiem tra app.log de biet chi tiet.
    pause
    goto MENU
)
timeout /t 1 >nul
set /a timeout-=1
if !timeout! gtr 0 goto CHECK_LOOP

echo [!] Da qua thoi gian cho (30s) nhung chua xac nhan duoc trang thai.
echo [!] Co the ung dung van dang khoi chay hoac co loi.
pause

:START_TUNNEL
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
        echo [+] Dang khoi dong Cloudflare Tunnel '!TUNNEL_NAME!' cho !MY_DOMAIN! tai cong !APP_PORT!...
        powershell -Command "Start-Process -FilePath 'cmd.exe' -ArgumentList '/c cloudflared tunnel run --url http://localhost:!APP_PORT! !TUNNEL_NAME! >> tunnel.log 2>&1' -WindowStyle Hidden"
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

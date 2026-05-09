#!/bin/bash

# ==========================================
# 1. AUTO DETECT ENVIRONMENT & VARIABLES
# ==========================================

# Internet check function
check_internet() {
    echo "[+] Checking internet connection..."
    if ! curl -fsSL --connect-timeout 5 https://api.github.com >/dev/null 2>&1; then
        echo "[!] No internet connection or github.com is unreachable!"
        exit 1
    fi
}

# CPU architecture normalization function
normalize_arch() {
    local arch
    # Prefer dpkg in Termux for better accuracy (avoids 32-bit on 64-bit kernel issues)
    if [ -n "$PREFIX" ] && command -v dpkg &>/dev/null; then
        arch=$(dpkg --print-architecture)
        case "$arch" in
            aarch64) echo "arm64" ;;
            arm)     echo "armv7" ;;
            i686)    echo "386" ;;
            x86_64)  echo "amd64" ;;
            *)       echo "$arch" ;;
        esac
    else
        arch=$(uname -m)
        case "$arch" in
            x86_64)          echo "amd64" ;;
            aarch64|arm64)   echo "arm64" ;;
            armv7l|armhf)    echo "armv7" ;;
            armv6l)          echo "armv6" ;;
            i386|i686)       echo "386" ;;
            *)               echo "$arch" ;;
        esac
    fi
}

# Detect package manager using /etc/os-release and available commands
detect_pkg_manager() {
    if [ -n "$PREFIX" ] && command -v pkg &>/dev/null; then
        PKG_MGR="pkg"
    elif command -v apt &>/dev/null; then
        PKG_MGR="apt"
    elif command -v dnf &>/dev/null; then
        PKG_MGR="dnf"
    elif command -v yum &>/dev/null; then
        PKG_MGR="yum"
    elif command -v pacman &>/dev/null; then
        PKG_MGR="pacman"
    elif command -v apk &>/dev/null; then
        PKG_MGR="apk"
    elif command -v zypper &>/dev/null; then
        PKG_MGR="zypper"
    elif command -v brew &>/dev/null; then
        PKG_MGR="brew"
    else
        echo "[!] Cannot detect package manager. Supported: apt, dnf, yum, pacman, apk, zypper, brew, pkg."
        exit 1
    fi

    # Read distro name for display
    DISTRO_NAME="Linux"
    if [ "$(uname -s)" == "Darwin" ]; then
        DISTRO_NAME="macOS $(sw_vers -productVersion)"
    elif [ -f /etc/os-release ]; then
        DISTRO_NAME=$(. /etc/os-release && echo "${PRETTY_NAME:-$NAME}")
    fi
    echo "[+] Operating System: $DISTRO_NAME (Package manager: $PKG_MGR)"
}

# Install a single package, skip if already installed
pkg_install() {
    local pkg="$1"
    local cmd="${2:-$pkg}"
    if command -v "$cmd" &>/dev/null; then
        echo "[✓] $pkg is already installed, skipping."
        return 0
    fi
    echo "[+] Installing $pkg..."
    case "$PKG_MGR" in
        apt)     apt install -y "$pkg" ;;
        dnf)     dnf install -y "$pkg" ;;
        yum)     yum install -y "$pkg" ;;
        pacman)  pacman -S --noconfirm "$pkg" ;;
        apk)     apk add --no-cache "$pkg" ;;
        zypper)  zypper install -y "$pkg" ;;
        brew)    brew install "$pkg" ;;
        pkg)     pkg install -y "$pkg" ;;
    esac
}

# File download function with wget/curl fallback and retry
download_file() {
    local url="$1"
    local output="$2"
    local retries=3
    local count=0
    
    while [ $count -lt $retries ]; do
        if command -v wget &>/dev/null; then
            wget -qO "$output" "$url" && return 0
        elif command -v curl &>/dev/null; then
            curl -fsSL "$url" -o "$output" && return 0
        else
            echo "[!] wget or curl is required to download files!"
            return 1
        fi
        count=$((count + 1))
        [ $count -lt $retries ] && echo "[!] Download failed, retrying ($count/$retries)..." && sleep 2
    done
    return 1
}

if [ -n "$PREFIX" ] && echo "$PREFIX" | grep -q "termux"; then
    OS_TYPE="termux"
    BASE_DIR="$HOME/telecloud-go"
    BIN_DIR="$PREFIX/bin"
    PKG_MGR="pkg"
    echo "[+] Operating System: Termux (Android)"
    
    echo "[+] Updating Termux system (pkg update & upgrade)..."
    pkg update -y && pkg upgrade -y

    # Check Termux version (Play Store versions are restricted and cause e_type errors)
    T_INFO=$(termux-info 2>/dev/null || echo "")
    T_VER=$(echo "$T_INFO" | grep "TERMUX_VERSION" | cut -d'=' -f2)
    T_VER=${T_VER:-$TERMUX_VERSION}
    T_VER=${T_VER:-unknown}

    if [[ "$T_VER" == *"googleplay"* ]] || [[ "$T_INFO" == *"googleplay"* ]] || [ "$T_VER" == "0.101" ]; then
        echo ""
        echo "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"
        echo "⚠️  IMPORTANT WARNING: GOOGLE PLAY TERMUX DETECTED"
        echo "----------------------------------------------------------------"
        echo "You are using Termux from Google Play ($T_VER)."
        echo "This version is restricted by Google's policies and CANNOT run"
        echo "Go applications like TeleCloud on Android 10+ (Error: e_type)."
        echo ""
        echo "HOW TO FIX:"
        echo "1. Uninstall the current Termux app."
        echo "2. Download and install the latest version from F-Droid or GitHub:"
        echo "https://github.com/termux/termux-app/releases"
        echo "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"
        echo ""
        read -p "[?] Do you want to continue despite potential errors? (y/n): " confirm_ps
        if [ "$confirm_ps" != "y" ]; then
            exit 1
        fi
        echo "[-] It won't run anyway, why so stubborn? Try it and see..."
        sleep 2
    fi

elif [ "$(uname -s)" == "Darwin" ]; then
    OS_TYPE="macos"
    BASE_DIR="$HOME/telecloud-go"
    
    # Prefer /opt/homebrew/bin for Apple Silicon, fallback /usr/local/bin
    if [ -d "/opt/homebrew/bin" ]; then
        BIN_DIR="/opt/homebrew/bin"
    else
        BIN_DIR="/usr/local/bin"
    fi

    if ! command -v brew &>/dev/null; then
        echo "[!] Homebrew is not installed. Please install it first:"
        echo "    /bin/bash -c \"\$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\""
        exit 1
    fi
    PKG_MGR="brew"
    echo "[+] Operating System: macOS $(sw_vers -productVersion) (Arch: $(uname -m), BIN_DIR: $BIN_DIR)"
else
    OS_TYPE="linux"
    BASE_DIR="/opt/telecloud-go"
    BIN_DIR="/usr/local/bin"

    if [ "$EUID" -ne 0 ]; then
        echo "[!] Linux environment requires root privileges (sudo). Please try again!"
        exit 1
    fi

    detect_pkg_manager

    # Update package lists (apt and pacman only)
    if [ "$PKG_MGR" == "apt" ]; then
        apt update -qq
    elif [ "$PKG_MGR" == "pacman" ]; then
        pacman -Sy --noconfirm
    fi
fi

SESSION="telecloud"

# ========================
# 2. INSTALL DEPENDENCIES
# ========================
install_dependencies() {
    echo "[+] Checking and installing required packages..."

    if [ "$OS_TYPE" == "linux" ]; then
        # Install base packages one by one, skipping already-installed ones
        for pkg in curl wget tar unzip jq tmux nano; do
            pkg_install "$pkg"
        done

        echo ""
        echo "[!] Note: FFmpeg is only used to generate video/audio thumbnails."
        echo "[!] On Exynos chips or weak devices, FFmpeg may cause errors or system hangs."
        read -p "[?] Do you want to install FFmpeg? (y/n): " install_ffmpeg
        [ "$install_ffmpeg" == "y" ] && pkg_install "ffmpeg"

        echo ""
        echo "[!] yt-dlp allows downloading video/audio from YouTube, Facebook, TikTok..."
        read -p "[?] Do you want to install yt-dlp? (y/n): " install_ytdlp
        if [ "$install_ytdlp" == "y" ]; then
            pkg_install "python3" "python3"
            # Try to install pip if not present
            if ! command -v pip3 &>/dev/null && ! python3 -m pip --version &>/dev/null; then
                echo "[+] Installing pip..."
                pkg_install "python3-pip" "pip3" || pkg_install "python-pip" "pip3"
            fi
            
            echo "[+] Installing/Updating yt-dlp via pip to get the latest version..."
            # Use --break-system-packages for newer Linux distros (Debian 12+, Ubuntu 23+)
            if python3 -m pip install -U yt-dlp --break-system-packages 2>/dev/null; then
                echo "[✓] yt-dlp installed successfully via pip."
            else
                # Fallback if --break-system-packages is not supported or pip is old
                python3 -m pip install -U yt-dlp || {
                    echo "[+] pip installation failed, downloading binary directly..."
                    download_file "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp" "$BIN_DIR/yt-dlp"
                    chmod +x "$BIN_DIR/yt-dlp"
                }
            fi
        fi

        # Only install Cloudflared if using Cloudflare Tunnel
        if [ "${TUNNEL_METHOD:-}" == "cloudflare" ]; then
            if ! command -v cloudflared &>/dev/null; then
                echo "[+] Installing Cloudflared..."
                CF_ARCH=$(normalize_arch)
                [ "$CF_ARCH" == "armv7" ] && CF_ARCH="arm"
                CF_URL="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${CF_ARCH}"
                download_file "$CF_URL" "$BIN_DIR/cloudflared" || return 1
                chmod +x "$BIN_DIR/cloudflared"
                if ! "$BIN_DIR/cloudflared" --version &>/dev/null; then
                    echo "[!] ERROR: cloudflared cannot run on this system (maybe noexec mount)."
                    return 1
                fi
                hash -r 2>/dev/null
                echo "[+] Cloudflared installed successfully!"
            else
                echo "[✓] cloudflared is already installed, skipping."
            fi
        fi
    else
        # Termux / macOS
        echo ""
        echo "[!] Note: FFmpeg is only used to generate video/audio thumbnails."
        echo "[!] On Exynos chips or weak devices, FFmpeg may cause errors or system hangs."
        read -p "[?] Do you want to install FFmpeg? (y/n): " install_ffmpeg

        echo ""
        echo "[!] yt-dlp allows downloading video/audio from YouTube, Facebook, TikTok..."
        read -p "[?] Do you want to install yt-dlp? (y/n): " install_ytdlp

        MAIN_PACKAGES="wget curl tar unzip tmux jq nano python procps"
        [ "${TUNNEL_METHOD:-}" == "cloudflare" ] && MAIN_PACKAGES="$MAIN_PACKAGES cloudflared"
        [ "$install_ffmpeg" == "y" ] && MAIN_PACKAGES="$MAIN_PACKAGES ffmpeg"

        for pkg in $MAIN_PACKAGES; do
            pkg_install "$pkg"
        done

        if [ "$install_ytdlp" == "y" ]; then
            echo "[+] Installing/Updating yt-dlp via pip..."
            python3 -m pip install -U yt-dlp 2>/dev/null || python -m pip install -U yt-dlp || {
                echo "[!] pip installation failed, trying package manager..."
                pkg_install "yt-dlp"
            }
        fi
    fi
}

# =============================
# 3. DOWNLOAD AND SAVE BINARY
# =============================
download_telecloud() {
    echo "[+] Fetching the latest release info from GitHub..."
    API_DATA=$(curl -fsSL --connect-timeout 10 "https://api.github.com/repos/dabeecao/telecloud-go/releases/latest" 2>/dev/null || echo "")
    
    if [ -z "$API_DATA" ]; then
        echo "[!] Cannot connect to GitHub API!"; return 1
    fi

    VERSION=$(echo "$API_DATA" | jq -r ".tag_name" 2>/dev/null || echo "null")
    if [ -z "$VERSION" ] || [ "$VERSION" == "null" ]; then
        echo "[!] Failed to fetch release info from GitHub!"; return 1
    fi

    TARGET=$(normalize_arch)
    OS_NAME="linux"
    [ "$OS_TYPE" == "macos" ] && OS_NAME="darwin"

    # Find suitable binary URL
    URL=$(echo "$API_DATA" | jq -r --arg os "$OS_NAME" --arg arch "$TARGET" '
        .assets[] | select(.name | contains($os) and contains($arch)) | .browser_download_url
    ' | head -n 1)

    # Fallback for amd64/x86_64
    if [ -z "$URL" ] && [ "$TARGET" == "amd64" ]; then
        URL=$(echo "$API_DATA" | jq -r --arg os "$OS_NAME" '
            .assets[] | select(.name | contains($os) and contains("x86_64")) | .browser_download_url
        ' | head -n 1)
    fi

    if [ -z "$URL" ] || [ "$URL" == "null" ]; then
        echo "[!] Binary not found for suitable $OS_NAME $TARGET!"; return 1
    fi

    echo "[+] Downloading version $VERSION..."
    download_file "$URL" telecloud.tar.gz || return 1
    mkdir -p "$BASE_DIR"
    tar -xzf telecloud.tar.gz -C "$BASE_DIR" || { echo "[!] Extraction failed!"; return 1; }

    if [ ! -f "$BASE_DIR/telecloud" ]; then
        echo "[!] 'telecloud' binary not found!"; return 1
    fi
    
    chmod +x "$BASE_DIR/telecloud"
    echo "$VERSION" > "$BASE_DIR/version.txt"
    rm -f telecloud.tar.gz
}

# =============================
# 4. CONFIGURE .ENV
# =============================
create_env() {
    if [ ! -f "$BASE_DIR/.env" ]; then
        echo "[+] Setting up .env configuration..."

        read -p "PORT [Default 8091]: " PORT
        PORT=${PORT:-8091}

        cat > "$BASE_DIR/.env" <<EOF
PORT=$PORT
EOF
        
        if command -v ffmpeg &> /dev/null; then
            echo "FFMPEG_PATH=ffmpeg" >> "$BASE_DIR/.env"
        else
            echo "FFMPEG_PATH=disabled" >> "$BASE_DIR/.env"
        fi

        if command -v yt-dlp &> /dev/null; then
            echo "YTDLP_PATH=yt-dlp" >> "$BASE_DIR/.env"
        else
            echo "YTDLP_PATH=disabled" >> "$BASE_DIR/.env"
        fi

        chmod 600 "$BASE_DIR/.env"
        echo "✅ .env configuration saved"
    fi
}

# =============================
# 5. CONFIGURE CLOUDFLARED
# =============================
cloudflared_setup() {
    if [ ! -f "$HOME/.cloudflared/cert.pem" ] && [ ! -f "/etc/cloudflared/cert.pem" ]; then
        echo "[!] You need to login to Cloudflare..."
        cloudflared tunnel login || return 1
    fi

    # Fetch or generate a random tunnel name
    if [ -f "$BASE_DIR/tunnel-name.txt" ]; then
        TUNNEL_NAME=$(cat "$BASE_DIR/tunnel-name.txt")
    else
        RAND_SUFFIX=$(LC_ALL=C tr -dc 'a-z0-9' < /dev/urandom | head -c 6)
        TUNNEL_NAME="telecloud-$RAND_SUFFIX"
        echo "$TUNNEL_NAME" > "$BASE_DIR/tunnel-name.txt"
    fi

    if [ ! -f "$BASE_DIR/tunnel.txt" ]; then
        echo "[+] Creating Cloudflare Tunnel: $TUNNEL_NAME..."
        cloudflared tunnel create "$TUNNEL_NAME" > "$BASE_DIR/tunnel.txt" || return 1
    fi

    read -p "Enter your domain (e.g., telecloud.domain.com) or press Enter to skip: " MY_DOMAIN
    if [ ! -z "$MY_DOMAIN" ]; then
        echo "[+] Routing DNS (Force)..."
        cloudflared tunnel route dns -f "$TUNNEL_NAME" "$MY_DOMAIN" || echo "[!] DNS routing failed. You can reconfigure it in the Menu."
        echo "$MY_DOMAIN" > "$BASE_DIR/domain.txt"
        echo "✅ DNS routed successfully!"
    fi
}


# =============================
# 6. INITIALIZE SERVICES / RUN SCRIPTS
# =============================
create_run_scripts() {
    local APP_PORT=$(grep "^PORT=" "$BASE_DIR/.env" | cut -d'=' -f2)
    APP_PORT=${APP_PORT:-8091}

    # Create systemd service for Linux with systemd
    if [ "$OS_TYPE" == "linux" ] && command -v systemctl &>/dev/null; then
        cat > /etc/systemd/system/telecloud.service <<EOF
[Unit]
Description=Telecloud Go Service
After=network.target

[Service]
Type=simple
WorkingDirectory=$BASE_DIR
ExecStart=$BASE_DIR/telecloud
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

        # Cloudflare Tunnel service (if configured)
        if [ -f "$BASE_DIR/tunnel.txt" ]; then
            local TUNNEL_NAME=$(cat "$BASE_DIR/tunnel-name.txt" 2>/dev/null || echo "telecloud-tunnel")
            cat > /etc/systemd/system/telecloud-tunnel.service <<EOF
[Unit]
Description=Telecloud Cloudflared Tunnel
After=network.target

[Service]
Type=simple
ExecStart=$(command -v cloudflared) tunnel run --url http://localhost:$APP_PORT $TUNNEL_NAME
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
        fi
        systemctl daemon-reload
    fi

    # Create run.sh for all OS types (Linux fallback + Termux/macOS)
    WAKELOCK=""
    [ "$OS_TYPE" == "termux" ] && WAKELOCK="termux-wake-lock"

    cat > "$BASE_DIR/run.sh" <<EOF
#!/bin/bash
$WAKELOCK
cd "$BASE_DIR" || exit 1
while true; do
    if [ -f "./telecloud" ]; then
        chmod +x "./telecloud"
        echo "[RUN] \$(date '+%Y-%m-%d %H:%M:%S') - Starting TeleCloud..." >> "$BASE_DIR/app.log"
        ./telecloud >> "$BASE_DIR/app.log" 2>&1
        EXIT_CODE=\$?
        echo "[RUN] \$(date '+%Y-%m-%d %H:%M:%S') - TeleCloud stopped (exit code: \$EXIT_CODE). Restarting in 3s..." >> "$BASE_DIR/app.log"
    else
        echo "[ERROR] Binary ./telecloud not found in \$(pwd)" >> "$BASE_DIR/app.log" 2>&1
        exit 1
    fi
    sleep 3
done
EOF
    chmod +x "$BASE_DIR/run.sh"

    cat > "$BASE_DIR/run-cloudflared.sh" <<EOF
#!/bin/bash
$WAKELOCK
cd "$BASE_DIR" || exit 1
TUNNEL_NAME=\$(cat "$BASE_DIR/tunnel-name.txt" 2>/dev/null || echo "telecloud-tunnel")
while true; do
    cloudflared tunnel run --url http://localhost:$APP_PORT \$TUNNEL_NAME >> "$BASE_DIR/tunnel.log" 2>&1
    sleep 3
done
EOF
    chmod +x "$BASE_DIR/run-cloudflared.sh"
}

# =============================
# 7. CREATE MANAGEMENT MENU
# =============================
create_menu() {
    # Check write permissions for BIN_DIR
    local SUDO_CMD=""
    if [ ! -w "$BIN_DIR" ] && [ "$OS_TYPE" != "termux" ]; then
        echo "[!] Sudo privileges required to install 'telecloud' command to $BIN_DIR"
        SUDO_CMD="sudo"
    fi

    # Backup old menu if it exists
    if [ -f "$BIN_DIR/telecloud" ]; then
        $SUDO_CMD cp "$BIN_DIR/telecloud" "$BIN_DIR/telecloud.bak" 2>/dev/null || true
    fi

    echo "[+] Creating management menu at $BIN_DIR/telecloud..."
    $SUDO_CMD bash -c "cat > '$BIN_DIR/telecloud'" <<'EOF'
#!/bin/bash
set -e

# --- HELPER FUNCTIONS ---
normalize_arch() {
    local arch
    if [ -n "$PREFIX" ] && command -v dpkg &>/dev/null; then
        arch=$(dpkg --print-architecture)
        case "$arch" in
            aarch64) echo "arm64" ;;
            arm)     echo "armv7" ;;
            i686)    echo "386" ;;
            x86_64)  echo "amd64" ;;
            *)       echo "$arch" ;;
        esac
    else
        arch=$(uname -m)
        case "$arch" in
            x86_64)          echo "amd64" ;;
            aarch64|arm64)   echo "arm64" ;;
            armv7l|armhf)    echo "armv7" ;;
            armv6l)          echo "armv6" ;;
            i386|i686)       echo "386" ;;
            *)               echo "$arch" ;;
        esac
    fi
}

download_file() {
    local url="$1"
    local output="$2"
    local retries=3
    local count=0
    while [ $count -lt $retries ]; do
        if command -v wget &>/dev/null; then
            wget -qO "$output" "$url" && return 0
        elif command -v curl &>/dev/null; then
            curl -fsSL "$url" -o "$output" && return 0
        fi
        count=$((count + 1))
        [ $count -lt $retries ] && sleep 2
    done
    return 1
}

if [ -n "$PREFIX" ] && echo "$PREFIX" | grep -q "termux"; then
    OS_TYPE="termux"
    BASE_DIR="$HOME/telecloud-go"
elif [ "$(uname -s)" == "Darwin" ]; then
    OS_TYPE="macos"
    BASE_DIR="$HOME/telecloud-go"
else
    OS_TYPE="linux"
    BASE_DIR="/opt/telecloud-go"
    if [ "$EUID" -ne 0 ]; then
        echo "Please run the command with root privileges (sudo telecloud)."
        exit 1
    fi
fi

SESSION="telecloud"

pause() {
    echo ""
    read -p "Press Enter to return to the Menu..."
}

check_status() {
    echo "=========================================="
    echo "               SYSTEM STATUS              "
    echo "=========================================="
    [ -f "$BASE_DIR/version.txt" ] && echo "📌 Version          : $(cat $BASE_DIR/version.txt)"
    
    if [ -f "$BASE_DIR/.env" ]; then
        APP_PORT=$(grep "^PORT=" "$BASE_DIR/.env" | cut -d'=' -f2)
        echo "📌 App Port         : ${APP_PORT:-8091}"
    fi

    if [ "$OS_TYPE" == "linux" ]; then
        if command -v systemctl &>/dev/null; then
            (systemctl is-active --quiet telecloud) && echo "✅ Telecloud App    : Running" || echo "❌ Telecloud App    : Stopped"
            (systemctl is-active --quiet telecloud-tunnel) && echo "✅ CF Tunnel        : Online" || echo "❌ CF Tunnel        : Offline"
        else
            # Linux without systemd → check via tmux + ps
            (tmux has-session -t $SESSION 2>/dev/null) && echo "✅ TMUX (Background): Running" || echo "❌ TMUX (Background): Stopped"
            if ps auxww 2>/dev/null | grep -v grep | grep -q "$BASE_DIR/telecloud"; then
                echo "✅ Telecloud App    : Running"
            else
                echo "❌ Telecloud App    : Stopped"
            fi
        fi
    else
        (tmux has-session -t $SESSION 2>/dev/null) && echo "✅ TMUX (Background): Running" || echo "❌ TMUX (Background): Stopped"
        # Use ps to find processes for Termux compatibility
        if ps auxww 2>/dev/null | grep -v grep | grep -q "$BASE_DIR/telecloud"; then
            echo "✅ Telecloud App    : Running"
        else
            echo "❌ Telecloud App    : Stopped"
        fi
        if ps auxww 2>/dev/null | grep -v grep | grep -q "cloudflared tunnel run"; then
            echo "✅ CF Tunnel        : Online"
        else
            echo "❌ CF Tunnel        : Offline"
        fi
    fi
    if [ -f "$BASE_DIR/domain.txt" ]; then
        echo "🔗 Domain           : https://$(cat $BASE_DIR/domain.txt)"
    else
        echo "🔗 Domain           : Not configured"
    fi
    echo "=========================================="
}

start_app() {
    # Always stop the application before starting to avoid duplicate instances (Restart logic)
    stop_app >/dev/null 2>&1 || true

    echo "[+] Starting the application..."
    if [ "$OS_TYPE" == "linux" ]; then
        if command -v systemctl &>/dev/null; then
            [ -f /etc/systemd/system/telecloud.service ] && systemctl enable --now telecloud || true
            [ -f /etc/systemd/system/telecloud-tunnel.service ] && [ -f "$BASE_DIR/tunnel.txt" ] && systemctl enable --now telecloud-tunnel || true
            
            echo "[+] Checking startup status (waiting up to 30s)..."
            sleep 2
            local timeout=28
            local success=0
            while [ $timeout -gt 0 ]; do
                if journalctl -u telecloud.service -n 50 | grep -q "Starting TeleCloud on port"; then
                    success=1; break
                fi
                if journalctl -u telecloud.service -n 50 | grep -q "TeleCloud shut down"; then
                    success=2; break
                fi
                # Check if the service is dead (failed/inactive)
                if ! systemctl is-active --quiet telecloud; then
                    success=3; break
                fi
                sleep 1
                timeout=$((timeout - 1))
            done

            if [ $success -eq 1 ]; then
                echo "✅ TeleCloud started successfully!"
            elif [ $success -eq 2 ]; then
                echo "❌ ERROR: TeleCloud has shut down. Please check the logs (Option 5)."
                return 1
            elif [ $success -eq 3 ]; then
                echo "❌ ERROR: TeleCloud failed to maintain an active state. Please check the logs (Option 5)."
                return 1
            else
                echo "⚠️  WARNING: Wait time exceeded (30s). Status unconfirmed."
                echo "The application might still be starting or encountered an error."
            fi
        else
            # Linux without systemd → fall back to tmux (same as macOS/Termux)
            echo "[!] systemctl not found, using tmux to run in background..."
            > "$BASE_DIR/app.log"
            tmux kill-session -t $SESSION 2>/dev/null || true
            sleep 1
            tmux new-session -d -s $SESSION "bash $BASE_DIR/run.sh"
            if [ $? -ne 0 ]; then
                echo "❌ ERROR: Failed to create TMUX session. Please check if tmux is installed."
                return 1
            fi
            [ -f "$BASE_DIR/tunnel.txt" ] && tmux split-window -h -t $SESSION "bash $BASE_DIR/run-cloudflared.sh" 2>/dev/null || true

            echo "[+] Checking startup status (waiting up to 30s)..."
            sleep 2
            local timeout=28
            local success=0
            while [ $timeout -gt 0 ]; do
                if grep -q "Starting TeleCloud on port" "$BASE_DIR/app.log" 2>/dev/null; then
                    success=1; break
                fi
                if grep -q "TeleCloud shut down" "$BASE_DIR/app.log" 2>/dev/null; then
                    success=2; break
                fi
                if ! tmux has-session -t $SESSION 2>/dev/null; then
                    success=3; break
                fi
                sleep 1
                timeout=$((timeout - 1))
            done

            if [ $success -eq 1 ]; then
                echo "✅ TeleCloud started successfully! (tmux mode)"
            elif [ $success -eq 2 ]; then
                echo "❌ ERROR: TeleCloud has shut down. Please check the logs (Option 6)."
                tail -n 10 "$BASE_DIR/app.log" 2>/dev/null
                return 1
            elif [ $success -eq 3 ]; then
                echo "❌ ERROR: TMUX session terminated unexpectedly. Please check the logs (Option 6)."
                tail -n 10 "$BASE_DIR/app.log" 2>/dev/null
                return 1
            else
                echo "⚠️  WARNING: Wait time exceeded (30s). App may still be starting."
                tail -n 5 "$BASE_DIR/app.log" 2>/dev/null
            fi
        fi
    else
        # Prevent nested spawning if already inside tmux
        if [ -n "$TMUX" ]; then
            echo "[!] WARNING: You are running inside a TMUX session."
            echo "Starting the application here will create nested TMUX sessions, which can be confusing."
            read -p "Do you still want to continue? (y/n): " confirm_tmux
            [ "$confirm_tmux" != "y" ] && return
        fi

        # Clear old log for fresh check
        > "$BASE_DIR/app.log"
        
        # Ensure old session is killed before creating a new one
        tmux kill-session -t $SESSION 2>/dev/null || true
        sleep 1
        tmux new-session -d -s $SESSION "bash $BASE_DIR/run.sh"
        if [ $? -ne 0 ]; then
            echo "❌ ERROR: Failed to create TMUX session. Please check if tmux is installed."
            return 1
        fi
        [ -f "$BASE_DIR/tunnel.txt" ] && tmux split-window -h -t $SESSION "bash $BASE_DIR/run-cloudflared.sh" 2>/dev/null || true
        
        echo "[+] Checking startup status (waiting up to 30s)..."
        sleep 2
        local timeout=28
        local success=0
        while [ $timeout -gt 0 ]; do
            if grep -q "Starting TeleCloud on port" "$BASE_DIR/app.log" 2>/dev/null; then
                success=1; break
            fi
            if grep -q "TeleCloud shut down" "$BASE_DIR/app.log" 2>/dev/null; then
                success=2; break
            fi
            # Check if the TMUX session has died (meaning the background runner stopped)
            if ! tmux has-session -t $SESSION 2>/dev/null; then
                success=3; break
            fi
            sleep 1
            timeout=$((timeout - 1))
        done

        if [ $success -eq 1 ]; then
            echo "✅ TeleCloud started successfully!"
        elif [ $success -eq 2 ]; then
            echo "❌ ERROR: TeleCloud has shut down. Please check the logs (Option 6)."
            echo "--- Last 10 log lines ---"
            tail -n 10 "$BASE_DIR/app.log" 2>/dev/null
            return 1
        elif [ $success -eq 3 ]; then
            echo "❌ ERROR: TMUX session terminated unexpectedly. Please check the logs (Option 6)."
            echo "--- Last 10 log lines ---"
            tail -n 10 "$BASE_DIR/app.log" 2>/dev/null
            return 1
        else
            echo "⚠️  WARNING: Wait time exceeded (30s). Status unconfirmed."
            echo "The application might still be starting or encountered an error."
            echo "--- Last 5 log lines ---"
            tail -n 5 "$BASE_DIR/app.log" 2>/dev/null
        fi
    fi
}

stop_app() {
    echo "[+] Stopping the application..."
    if [ "$OS_TYPE" == "linux" ]; then
        if command -v systemctl &>/dev/null; then
            systemctl stop telecloud telecloud-tunnel 2>/dev/null || true
        else
            # Linux without systemd → use tmux
            tmux kill-session -t $SESSION 2>/dev/null || true
            local timeout=15
            while [ $timeout -gt 0 ]; do
                if ! ps auxww 2>/dev/null | grep -v grep | grep -q "$BASE_DIR/telecloud"; then
                    break
                fi
                sleep 1
                timeout=$((timeout - 1))
            done
            for pid in $(ps auxww 2>/dev/null | grep -v grep | grep "$BASE_DIR/run.sh" | awk '{print $2}'); do
                kill "$pid" 2>/dev/null || true
            done
            for pid in $(ps auxww 2>/dev/null | grep -v grep | grep "$BASE_DIR/telecloud" | awk '{print $2}'); do
                kill -9 "$pid" 2>/dev/null || true
            done
            for pid in $(ps auxww 2>/dev/null | grep -v grep | grep "cloudflared tunnel run" | awk '{print $2}'); do
                kill -9 "$pid" 2>/dev/null || true
            done
        fi
    else
        # Stop Tmux session (Tmux sends SIGHUP; app is configured to catch SIGHUP and shut down cleanly)
        tmux kill-session -t $SESSION 2>/dev/null || true
        
        # Wait for the process to exit completely (max 15s)
        local timeout=15
        while [ $timeout -gt 0 ]; do
            # Use ps auxww to show full path (prevents truncation on macOS)
            if ! ps auxww 2>/dev/null | grep -v grep | grep -q "$BASE_DIR/telecloud"; then
                break
            fi
            sleep 1
            timeout=$((timeout - 1))
        done
        
        # Force stop if anything remains (fallback - kill by PID)
        for pid in $(ps auxww 2>/dev/null | grep -v grep | grep "$BASE_DIR/run.sh" | awk '{print $2}'); do
            kill "$pid" 2>/dev/null || true
        done
        for pid in $(ps auxww 2>/dev/null | grep -v grep | grep "$BASE_DIR/run-cloudflared.sh" | awk '{print $2}'); do
            kill "$pid" 2>/dev/null || true
        done
        for pid in $(ps auxww 2>/dev/null | grep -v grep | grep "$BASE_DIR/telecloud" | awk '{print $2}'); do
            kill -9 "$pid" 2>/dev/null || true
        done
        for pid in $(ps auxww 2>/dev/null | grep -v grep | grep "cloudflared tunnel run" | awk '{print $2}'); do
            kill -9 "$pid" 2>/dev/null || true
        done
    fi
    echo "✅ Stopped everything."
}

restart_app() {
    start_app
}

manage_tunnel() {
    echo "=========================================="
    echo "        MANAGE REMOTE ACCESS"
    echo "=========================================="
    echo "--- Cloudflare Tunnel ---"
    echo "  1. Install / Reconfigure Cloudflare Tunnel"
    echo "  2. Remove Cloudflare Tunnel"
    echo "  3. Go back"
    read -p "Choose an option (1-3): " tc
    case $tc in
        1)
            if ! command -v cloudflared &>/dev/null; then
                echo "[+] Installing cloudflared..."
                if [ "$OS_TYPE" == "termux" ]; then
                    pkg install -y cloudflared
                elif [ "$OS_TYPE" == "macos" ]; then
                    brew install cloudflared
                else
                    local ARCH=$(normalize_arch)
                    local CF_BIN="/usr/local/bin/cloudflared"
                    download_file "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${ARCH}" "$CF_BIN"
                    chmod +x "$CF_BIN"
                fi
                if ! command -v cloudflared &>/dev/null; then
                    echo "❌ Error: Could not install cloudflared. Please install it manually."
                    return 1
                fi
            fi

            if [ ! -f "$HOME/.cloudflared/cert.pem" ] && [ ! -f "/etc/cloudflared/cert.pem" ]; then
                cloudflared tunnel login
            fi

            # Fetch or generate a random tunnel name
            if [ -f "$BASE_DIR/tunnel-name.txt" ]; then
                TUNNEL_NAME=$(cat "$BASE_DIR/tunnel-name.txt")
            else
                RAND_SUFFIX=$(LC_ALL=C tr -dc 'a-z0-9' < /dev/urandom | head -c 6)
                TUNNEL_NAME="telecloud-$RAND_SUFFIX"
                echo "$TUNNEL_NAME" > "$BASE_DIR/tunnel-name.txt"
            fi

            if [ ! -f "$BASE_DIR/tunnel.txt" ]; then
                echo "[+] Creating tunnel: $TUNNEL_NAME..."
                cloudflared tunnel create "$TUNNEL_NAME" > "$BASE_DIR/tunnel.txt"
            fi

            read -p "Enter the domain to route (e.g., telecloud.domain.com): " NEW_DOMAIN
            if [ ! -z "$NEW_DOMAIN" ]; then
                cloudflared tunnel route dns -f "$TUNNEL_NAME" "$NEW_DOMAIN"
                if [ $? -eq 0 ]; then
                    echo "$NEW_DOMAIN" > "$BASE_DIR/domain.txt"
                    echo "✅ DNS routed successfully! (Please restart the app to apply)"
                else
                    echo "❌ Error routing DNS."
                fi
            fi
            ;;
        2)
            TUNNEL_NAME=$(cat "$BASE_DIR/tunnel-name.txt" 2>/dev/null || echo "telecloud-tunnel")
            if [ "$OS_TYPE" == "linux" ]; then
                if command -v systemctl &>/dev/null; then
                    systemctl stop telecloud-tunnel 2>/dev/null
                    systemctl disable telecloud-tunnel 2>/dev/null
                fi
            else
                for pid in $(ps auxww 2>/dev/null | grep -v grep | grep "cloudflared tunnel run" | awk '{print $2}'); do
                    kill "$pid" 2>/dev/null || true
                done
            fi
            echo "[+] Removing tunnel $TUNNEL_NAME from Cloudflare..."
            cloudflared tunnel delete -f "$TUNNEL_NAME" 2>/dev/null
            rm -f "$BASE_DIR/tunnel.txt"
            rm -f "$BASE_DIR/domain.txt"
            rm -f "$BASE_DIR/tunnel-name.txt"
            echo "✅ Tunnel removed."
            echo "📢 Remember to remove the old DNS record at dash.cloudflare.com!"
            ;;
        *) return ;;
    esac
}

view_logs() {
    echo "=========================================="
    echo "               SYSTEM LOGS                "
    echo "=========================================="
    echo "1. View App Logs (Telecloud)"
    echo "2. View Cloudflare Tunnel Logs"
    echo "3. Go back"
    read -p "Choose a log (1-3): " log_choice

    if [[ "$log_choice" == "1" || "$log_choice" == "2" ]]; then
        echo "💡 TIP: Press Ctrl+C to exit log view."
        echo "After exiting, if the menu closes, run 'telecloud' again."
        echo "Loading logs..."
        sleep 2
    fi

    case $log_choice in
        1)
            if [ "$OS_TYPE" == "linux" ]; then
                if command -v systemctl &>/dev/null; then
                    journalctl -u telecloud.service -f -n 50
                else
                    echo "[!] journalctl not available."
                fi
            else
                [ -f "$BASE_DIR/app.log" ] && tail -f -n 50 "$BASE_DIR/app.log" || echo "❌ No app log file found."
            fi
            ;;
        2)
            if [ "$OS_TYPE" == "linux" ]; then
                if command -v systemctl &>/dev/null; then
                    journalctl -u telecloud-tunnel.service -f -n 50
                else
                    echo "[!] journalctl not available."
                fi
            else
                [ -f "$BASE_DIR/tunnel.log" ] && tail -f -n 50 "$BASE_DIR/tunnel.log" || echo "❌ No tunnel log file found."
            fi
            ;;
        *) return ;;
    esac
}

edit_env() {
    echo "=========================================="
    echo "           EDIT CONFIG (.ENV)             "
    echo "=========================================="
    if [ ! -f "$BASE_DIR/.env" ]; then
        echo "❌ .env file not found at $BASE_DIR!"
        return
    fi

    if command -v nano >/dev/null 2>&1; then
        nano "$BASE_DIR/.env"
    elif command -v vi >/dev/null 2>&1; then
        vi "$BASE_DIR/.env"
    else
        echo "❌ nano or vi is required to edit the config!"
        return
    fi

    echo "✅ Configuration saved!"
    read -p "Do you want to restart the app now to apply changes? (y/n): " rs
    if [ "$rs" == "y" ]; then
        stop_app
        start_app
    fi
}

backup_data() {
    echo "=========================================="
    echo "               DATA BACKUP                "
    echo "=========================================="
    mkdir -p "$HOME/telecloud_backups"
    local BK_NAME="telecloud_backup_$(date +%Y%m%d_%H%M%S).tar.gz"
    
    echo "[+] Stopping application to ensure data integrity..."
    stop_app
    echo "[+] Creating backup..."
    (cd "$BASE_DIR" && tar -czf "$HOME/telecloud_backups/$BK_NAME" database.db* .env 2>/dev/null)
    
    if [ $? -eq 0 ]; then
        echo "✅ Backup successful: $HOME/telecloud_backups/$BK_NAME"
    else
        echo "❌ Error: Some files (database.db) might not exist yet."
    fi
    start_app
}

restore_data() {
    echo "=========================================="
    echo "               DATA RESTORE               "
    echo "=========================================="
    if [ ! -d "$HOME/telecloud_backups" ] || [ -z "$(ls -A $HOME/telecloud_backups)" ]; then
        echo "❌ No backups found in $HOME/telecloud_backups"
        return
    fi

    echo "Available backups:"
    ls -1 "$HOME/telecloud_backups"
    echo ""
    read -p "Enter filename to restore (e.g., telecloud_backup_...tar.gz): " FILE_NAME
    
    if [ ! -f "$HOME/telecloud_backups/$FILE_NAME" ]; then
        echo "❌ File does not exist!"
        return
    fi

    read -p "⚠️ Restoration will overwrite current data. Continue? (y/n): " cf
    if [ "$cf" == "y" ]; then
        stop_app
        echo "[+] Cleaning up old data..."
        rm -f "$BASE_DIR/database.db" "$BASE_DIR/database.db-wal" "$BASE_DIR/database.db-shm" 2>/dev/null || true
        (cd "$BASE_DIR" && tar -xzf "$HOME/telecloud_backups/$FILE_NAME")
        echo "✅ Restoration complete. Please restart the application."
    fi
}

manage_backups() {
    echo "=========================================="
    echo "              MANAGE BACKUP               "
    echo "=========================================="
    echo "1. Create new backup"
    echo "2. Restore from old backup"
    echo "3. Go back"
    read -p "Choose an option (1-3): " b_choice
    case $b_choice in
        1) backup_data ;;
        2) restore_data ;;
        *) return ;;
    esac
}

update_app() {
    echo "[+] Checking for updates..."
    API_DATA=$(curl -fsSL --connect-timeout 10 "https://api.github.com/repos/dabeecao/telecloud-go/releases/latest" 2>/dev/null || echo "")
    
    if [ -z "$API_DATA" ]; then
        echo "❌ Error: Cannot fetch data from GitHub API!"; return
    fi

    LATEST=$(echo "$API_DATA" | jq -r ".tag_name" 2>/dev/null || echo "null")
    LOCAL=$(cat "$BASE_DIR/version.txt" 2>/dev/null)

    if [ "$LATEST" == "null" ]; then
        echo "❌ Error: Could not identify version from GitHub."; return
    fi

    if [ "$LATEST" == "$LOCAL" ]; then
        echo "✅ You are on the latest version ($LOCAL)."
        return
    fi

    echo "🔥 New version available: $LATEST. Updating..."
    TARGET=$(normalize_arch)
    OS_NAME="linux"
    [ "$OS_TYPE" == "macos" ] && OS_NAME="darwin"

    # Find suitable binary URL
    URL=$(echo "$API_DATA" | jq -r --arg os "$OS_NAME" --arg arch "$TARGET" '
        .assets[] | select(.name | contains($os) and contains($arch)) | .browser_download_url
    ' | head -n 1)

    # Fallback for amd64/x86_64
    if [ -z "$URL" ] && [ "$TARGET" == "amd64" ]; then
        URL=$(echo "$API_DATA" | jq -r --arg os "$OS_NAME" '
            .assets[] | select(.name | contains($os) and contains("x86_64")) | .browser_download_url
        ' | head -n 1)
    fi

    if [ -z "$URL" ] || [ "$URL" == "null" ]; then
        echo "❌ Error: Binary not found for $OS_NAME $TARGET."
        return
    fi

    echo "Downloading update..."
    download_file "$URL" telecloud.tar.gz || { echo "❌ Error downloading file!"; return; }
    
    stop_app
    # Backup old file to avoid overwrite issues with running process
    [ -f "$BASE_DIR/telecloud" ] && mv "$BASE_DIR/telecloud" "$BASE_DIR/telecloud.old"
    tar -xzf telecloud.tar.gz -C "$BASE_DIR" || { 
        echo "❌ Error extracting file!"
        [ -f "$BASE_DIR/telecloud.old" ] && mv "$BASE_DIR/telecloud.old" "$BASE_DIR/telecloud"
        return
    }
    
    chmod +x "$BASE_DIR/telecloud"
    echo "$LATEST" > "$BASE_DIR/version.txt"
    rm -f telecloud.tar.gz "$BASE_DIR/telecloud.old" 2>/dev/null
    hash -r 2>/dev/null
    echo "✅ Update complete. Please choose Restart."
}

update_setup_script() {
    echo "[+] Checking for management script updates..."
    local SCRIPT_URL="https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-setup-en.sh"
    # Download temporary file
    if download_file "$SCRIPT_URL" "$BASE_DIR/auto-setup-en.sh.new"; then
        mv "$BASE_DIR/auto-setup-en.sh.new" "$BASE_DIR/auto-setup-en.sh"
        chmod +x "$BASE_DIR/auto-setup-en.sh"
        echo "✅ Updated auto-setup-en.sh successfully."
        # Call the script itself to update BIN_DIR
        bash "$BASE_DIR/auto-setup-en.sh" --update-menu
        echo "✅ Updated 'telecloud' command menu successfully."
        echo "[!] Please exit and run 'telecloud' again to apply changes."
    else
        echo "❌ Error: Failed to download update from GitHub."
    fi
}

telecloud_commands() {
    echo "=========================================="
    echo "            TELECLOUD COMMANDS            "
    echo "=========================================="
    echo "1. Reset Password (-resetpass)"
    echo "2. Update this Setup Script"
    echo "3. Return to Main Menu"
    read -p "Choose a command (1-3): " cmd_choice
    
    case $cmd_choice in
        1)
            echo "[+] Resetting password..."
            "$BASE_DIR/telecloud" -resetpass
            ;;
        2)
            update_setup_script
            ;;
        *) return ;;
    esac
}

uninstall() {
    echo "⚠️ WARNING: You are about to completely remove the application and all its data."
    read -p "Confirm uninstallation? (y/n): " cf
    if [ "$cf" == "y" ]; then
        echo "[+] Stopping the application..."
        stop_app

        # Release wake lock for Termux
        if [ "$OS_TYPE" == "termux" ] && command -v termux-wake-unlock &>/dev/null; then
            termux-wake-unlock
        fi

        if [ -f "$BASE_DIR/tunnel-name.txt" ]; then
            TUNNEL_NAME=$(cat "$BASE_DIR/tunnel-name.txt")
            echo "[+] Removing Tunnel '$TUNNEL_NAME' from Cloudflare..."
            cloudflared tunnel delete -f "$TUNNEL_NAME" 2>/dev/null || true
            
            echo "------------------------------------------------------"
            echo "📢 IMPORTANT NOTE:"
            echo "The script has removed the Tunnel from the system,"
            echo "but the DNS record on the Cloudflare Dashboard"
            echo "may still exist."
            echo "Remember to visit dash.cloudflare.com to remove"
            echo "the old DNS record to avoid system clutter."
            echo "------------------------------------------------------"
        fi
        
        if [ "$OS_TYPE" == "linux" ] && command -v systemctl &>/dev/null; then
            echo "[+] Removing systemd services..."
            systemctl stop telecloud telecloud-tunnel 2>/dev/null || true
            systemctl disable telecloud telecloud-tunnel 2>/dev/null || true
            rm -f /etc/systemd/system/telecloud.service 2>/dev/null || true
            rm -f /etc/systemd/system/telecloud-tunnel.service 2>/dev/null || true
            systemctl daemon-reload 2>/dev/null || true
        fi
        
        echo "[+] Deleting application directory: $BASE_DIR"
        if [ -n "$BASE_DIR" ] && [ "$BASE_DIR" != "/" ] && [ -d "$BASE_DIR" ]; then
            rm -rf "$BASE_DIR"
        fi

        echo "[+] Deleting 'telecloud' command..."
        if [ -n "$BIN_DIR" ]; then
            rm -f "$BIN_DIR/telecloud" 2>/dev/null || true
            rm -f "$BIN_DIR/telecloud.bak" 2>/dev/null || true
        fi
        
        echo "✅ Application successfully uninstalled."
        exit
    fi
}

while true; do
    clear
    echo "=========================================="
    echo "         TELECLOUD MANAGER MENU           "
    echo "=========================================="
    echo "  1. System Status"
    echo "  2. Start Application"
    echo "  3. Stop Application"
    echo "  4. Restart Application"
    echo "  5. Manage Remote Access (Cloudflare Tunnel)"
    echo "  6. View Logs"
    echo "  7. Edit Config (.env)"
    echo "  8. Telecloud Commands (Reset Pass / Setup)"
    echo "  9. Check for Updates"
    echo "  10. Manage Backups"
    echo "  11. Uninstall Application"
    echo "  12. Exit"
    echo "=========================================="
    read -p "Choose an option (1-12): " c
    case $c in
        1) check_status; pause ;;
        2) start_app; pause ;;
        3) stop_app; pause ;;
        4) restart_app; pause ;;
        5) manage_tunnel; pause ;;
        6) view_logs ;;
        7) edit_env; pause ;;
        8) telecloud_commands; pause ;;
        9) update_app; pause ;;
        10) manage_backups; pause ;;
        11) uninstall ;;
        12) clear; exit ;;
        *) echo "[!] Invalid choice!"; pause ;;
    esac
done
EOF
    $SUDO_CMD chmod +x "$BIN_DIR/telecloud"
}

# =============================
# MAIN EXECUTION BLOCK
# =============================
set -e
rollback() {
    echo -e "\n[!] INSTALLATION ERROR! Cleaning up..."
    [ -n "$BASE_DIR" ] && [ "$BASE_DIR" != "/" ] && rm -rf "$BASE_DIR"
    rm -f telecloud.tar.gz 2>/dev/null
    exit 1
}

# Command-line argument handling
if [ "$1" == "--update-menu" ]; then
    create_menu
    exit 0
fi

if [ ! -f "$BASE_DIR/telecloud" ]; then
    check_internet
    echo "--- FIRST TIME TELECLOUD INSTALLATION ---"
    echo ""
    echo "Use Cloudflare Tunnel for remote access?"
    read -p "Choose (y/n) [Default y]: " _tm
    _tm=${_tm:-y}
    if [ "$_tm" == "y" ]; then
        TUNNEL_METHOD="cloudflare"
    else
        TUNNEL_METHOD="none"
    fi
    export TUNNEL_METHOD

    trap rollback INT TERM
    install_dependencies || rollback
    download_telecloud || rollback
    create_env || rollback

    if [ "$TUNNEL_METHOD" == "cloudflare" ]; then
        cloudflared_setup || rollback
    fi

    create_run_scripts || rollback
    create_menu || rollback
    trap - INT TERM

    echo "============================================="
    echo "✅ INSTALLATION SUCCESSFUL!"
    echo "Type the following command to open the Management Menu:"
    echo "   telecloud"
    echo ""
    PORT=$(grep "^PORT=" "$BASE_DIR/.env" | cut -d'=' -f2)
    PORT=${PORT:-8091}
    echo "The application is running in Setup Mode."
    echo "Please visit the following URL to complete the setup:"
    if [ -f "$BASE_DIR/domain.txt" ]; then
        echo "   https://$(cat $BASE_DIR/domain.txt)/setup"
    else
        echo "   http://YOUR_IP_OR_DOMAIN:$PORT/setup"
    fi
    echo "============================================="
    exit 0
fi

# Re-create menu if missing but binary exists
if [ ! -f "$BIN_DIR/telecloud" ]; then
    echo "[!] Binary detected but 'telecloud' command is missing."
    create_menu
fi

# Execute menu
"$BIN_DIR/telecloud"
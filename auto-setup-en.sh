#!/bin/bash

# ==========================================
# 1. AUTO DETECT ENVIRONMENT & VARIABLES
# ==========================================

# Detect package manager using /etc/os-release and available commands
detect_pkg_manager() {
    if command -v apt &>/dev/null; then
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
        echo "[!] Cannot detect package manager. Supported: apt, dnf, yum, pacman, apk, zypper, brew."
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
    esac
}

if [ -n "$PREFIX" ] && echo "$PREFIX" | grep -q "termux"; then
    OS_TYPE="termux"
    BASE_DIR="$HOME/telecloud-go"
    BIN_DIR="$PREFIX/bin"
    PKG_MGR="pkg"
    echo "[+] Operating System: Termux (Android)"
elif [ "$(uname -s)" == "Darwin" ]; then
    OS_TYPE="macos"
    BASE_DIR="$HOME/telecloud-go"
    BIN_DIR="/usr/local/bin"
    if ! command -v brew &>/dev/null; then
        echo "[!] Homebrew is not installed. Please install it first:"
        echo "    /bin/bash -c \"\$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\""
        exit 1
    fi
    PKG_MGR="brew"
    echo "[+] Operating System: macOS $(sw_vers -productVersion) (Package manager: brew)"
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

        # Install Cloudflared if not present
        if ! command -v cloudflared &>/dev/null; then
            echo "[+] Installing Cloudflared..."
            if [ "$OS_TYPE" == "macos" ]; then
                brew install cloudflared || { echo "[!] Failed to install cloudflared via brew!"; return 1; }
            else
                ARCH=$(uname -m)
                case "$ARCH" in
                    x86_64)        ARCH="amd64" ;;
                    aarch64|arm64) ARCH="arm64" ;;
                    armv7l|armhf)  ARCH="armv7" ;;
                    *)
                        echo "[!] Unsupported architecture for Cloudflared: $ARCH"
                        return 1
                    ;;
                esac
                CF_URL="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${ARCH}"
                # Try wget first, fallback to curl
                if command -v wget &>/dev/null; then
                    wget -qO /usr/local/bin/cloudflared "$CF_URL" || { echo "[!] Failed to download cloudflared!"; return 1; }
                elif command -v curl &>/dev/null; then
                    curl -fsSL "$CF_URL" -o /usr/local/bin/cloudflared || { echo "[!] Failed to download cloudflared!"; return 1; }
                else
                    echo "[!] wget or curl is required to download Cloudflared!"; return 1
                fi
                chmod +x /usr/local/bin/cloudflared
            fi
            echo "[+] Cloudflared installed successfully!"
        else
            echo "[✓] cloudflared is already installed, skipping."
        fi
    else
        # Termux
        echo ""
        echo "[!] Note: FFmpeg is only used to generate video/audio thumbnails."
        echo "[!] On Exynos chips or weak devices, FFmpeg may cause errors or system hangs."
        read -p "[?] Do you want to install FFmpeg? (y/n): " install_ffmpeg

        MAIN_PACKAGES="wget curl tar unzip tmux cloudflared jq nano"
        [ "$install_ffmpeg" == "y" ] && MAIN_PACKAGES="$MAIN_PACKAGES ffmpeg"

        for pkg in $MAIN_PACKAGES; do
            if ! command -v "$pkg" &>/dev/null; then
                echo "[+] Installing $pkg..."
                pkg install -y "$pkg" || return 1
            else
                echo "[✓] $pkg is already installed, skipping."
            fi
        done
    fi
}

# =============================
# 3. DOWNLOAD AND SAVE BINARY
# =============================
download_telecloud() {
    echo "[+] Fetching the latest release info from GitHub..."
    API_DATA=$(curl -fsSL "https://api.github.com/repos/dabeecao/telecloud-go/releases/latest")
    
    VERSION=$(echo "$API_DATA" | jq -r ".tag_name")
    if [ -z "$VERSION" ] || [ "$VERSION" == "null" ]; then
        echo "[!] Failed to fetch release info from GitHub!"; return 1
    fi

    TARGET=$(uname -m)

    if [[ "$TARGET" == "aarch64" || "$TARGET" == "arm64" ]]; then
        TARGET="arm64"
    elif [[ "$TARGET" == "x86_64" ]]; then
        TARGET="amd64"
    elif [[ "$TARGET" == "armv7l" || "$TARGET" == "armhf" ]]; then
        TARGET="armv7"
    fi

    if [ "$TARGET" == "arm64" ]; then
        URL=$(echo "$API_DATA" | jq -r '.assets[] | select(.name | contains("linux_arm64")) | .browser_download_url')
    elif [ "$TARGET" == "amd64" ]; then
        URL=$(echo "$API_DATA" | jq -r '.assets[] | select(.name | contains("linux_amd64") or contains("linux_x86_64")) | .browser_download_url')
    elif [ "$TARGET" == "armv7" ]; then
        URL=$(echo "$API_DATA" | jq -r '.assets[] | select(.name | contains("linux_armv7")) | .browser_download_url')
    fi

    if [ -z "$URL" ] || [ "$URL" == "null" ]; then
        echo "[!] Binary not found for architecture $TARGET!"; return 1
    fi

    echo "[+] Downloading version $VERSION..."
    wget -qO telecloud.tar.gz "$URL" || return 1
    mkdir -p "$BASE_DIR"
    tar -xzf telecloud.tar.gz -C "$BASE_DIR" || return 1

    if [ ! -f "$BASE_DIR/telecloud" ]; then
        echo "[!] 'telecloud' binary not found!"; return 1
    fi
    
    echo "$VERSION" > "$BASE_DIR/version.txt"
    rm telecloud.tar.gz
}

# =============================
# 4. CONFIGURE .ENV
# =============================
create_env() {
    if [ ! -f "$BASE_DIR/.env" ]; then
        echo "[+] Setting up .env configuration..."

        API_ID=""
        while [ -z "$API_ID" ]; do
            read -p "Enter API_ID (Required): " API_ID
        done

        API_HASH=""
        while [ -z "$API_HASH" ]; do
            read -p "Enter API_HASH (Required): " API_HASH
        done

        read -p "PORT [Default 8091]: " PORT
        PORT=${PORT:-8091}

        read -p "LOG_GROUP_ID [Default me]: " LOG_GROUP_ID
        LOG_GROUP_ID=${LOG_GROUP_ID:-me}

        read -p "MAX_UPLOAD_SIZE_MB [Default 2000]: " MAX_UPLOAD
        MAX_UPLOAD=${MAX_UPLOAD:-2000}

        cat > "$BASE_DIR/.env" <<EOF
API_ID=$API_ID
API_HASH=$API_HASH
LOG_GROUP_ID=$LOG_GROUP_ID
PORT=$PORT
MAX_UPLOAD_SIZE_MB=$MAX_UPLOAD
EOF
        
        if command -v ffmpeg &> /dev/null; then
            echo "FFMPEG_PATH=ffmpeg" >> "$BASE_DIR/.env"
        else
            echo "FFMPEG_PATH=disabled" >> "$BASE_DIR/.env"
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

    if [ ! -f "$BASE_DIR/tunnel.txt" ]; then
        echo "[+] Creating Cloudflare Tunnel..."
        cloudflared tunnel create telecloud-tunnel > "$BASE_DIR/tunnel.txt" || return 1
    fi

    read -p "Enter your domain (e.g., telecloud.domain.com) or press Enter to skip: " MY_DOMAIN
    if [ ! -z "$MY_DOMAIN" ]; then
        echo "[+] Routing DNS (Force)..."
        cloudflared tunnel route dns -f telecloud-tunnel "$MY_DOMAIN" || echo "[!] DNS routing failed. You can reconfigure it in the Menu."
        echo "$MY_DOMAIN" > "$BASE_DIR/domain.txt"
        echo "✅ DNS routed successfully!"
    fi
}

# =============================
# 6. INITIALIZE SERVICES / RUN SCRIPTS
# =============================
create_run_scripts() {
    local APP_PORT=$(grep PORT "$BASE_DIR/.env" | cut -d'=' -f2)
    APP_PORT=${APP_PORT:-8091}

    if [ "$OS_TYPE" == "linux" ]; then
        # Configure Systemd for Linux
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

        cat > /etc/systemd/system/telecloud-tunnel.service <<EOF
[Unit]
Description=Telecloud Cloudflared Tunnel
After=network.target

[Service]
Type=simple
ExecStart=$(command -v cloudflared) tunnel run --url http://localhost:$APP_PORT telecloud-tunnel
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
        systemctl daemon-reload
    else
        # Configure Tmux for Termux / macOS
        # Termux needs wake-lock; macOS/others skip it
        WAKELOCK=""
        [ "$OS_TYPE" == "termux" ] && WAKELOCK="termux-wake-lock"

        cat > "$BASE_DIR/run.sh" <<EOF
#!/bin/bash
$WAKELOCK
while true; do
    ./telecloud >> "$BASE_DIR/app.log" 2>&1
    sleep 3
done
EOF
        chmod +x "$BASE_DIR/run.sh"

        cat > "$BASE_DIR/run-cloudflared.sh" <<EOF
#!/bin/bash
$WAKELOCK
while true; do
    cloudflared tunnel run --url http://localhost:$APP_PORT telecloud-tunnel >> "$BASE_DIR/tunnel.log" 2>&1
    sleep 3
done
EOF
        chmod +x "$BASE_DIR/run-cloudflared.sh"
    fi
}

# =============================
# 7. CREATE MANAGEMENT MENU
# =============================
create_menu() {
    cat > "$BIN_DIR/telecloud" <<'EOF'
#!/bin/bash

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
        APP_PORT=$(grep PORT "$BASE_DIR/.env" | cut -d'=' -f2)
        echo "📌 App Port         : ${APP_PORT:-8091}"
    fi

    if [ "$OS_TYPE" == "linux" ]; then
        systemctl is-active --quiet telecloud && echo "✅ Telecloud App    : Running" || echo "❌ Telecloud App    : Stopped"
        systemctl is-active --quiet telecloud-tunnel && echo "✅ CF Tunnel        : Online" || echo "❌ CF Tunnel        : Offline"
    else
        # Termux and macOS both use tmux
        tmux has-session -t $SESSION 2>/dev/null && echo "✅ TMUX (Background): Running" || echo "❌ TMUX (Background): Stopped"
        pgrep -f "\./telecloud" > /dev/null && echo "✅ Telecloud App    : Running" || echo "❌ Telecloud App    : Stopped"
        pgrep -f "cloudflared tunnel run" > /dev/null && echo "✅ CF Tunnel        : Online" || echo "❌ CF Tunnel        : Offline"
    fi
    
    if [ -f "$BASE_DIR/domain.txt" ]; then
        echo "🔗 Domain           : https://$(cat $BASE_DIR/domain.txt)"
    else
        echo "🔗 Domain           : Not configured"
    fi
    echo "=========================================="
}

start_app() {
    if [ ! -f "$BASE_DIR/session.json" ]; then
        echo "❌ ERROR: You are not logged in to Telegram!"
        echo "Please select Option 8: 'Telecloud Commands' -> 'Initial Login' first."
        return 1
    fi

    echo "[+] Starting the application..."
    if [ "$OS_TYPE" == "linux" ]; then
        systemctl enable --now telecloud
        [ -f "$BASE_DIR/tunnel.txt" ] && systemctl enable --now telecloud-tunnel
    else
        tmux new-session -d -s $SESSION "cd $BASE_DIR && ./run.sh"
        [ -f "$BASE_DIR/tunnel.txt" ] && tmux split-window -h -t $SESSION "cd $BASE_DIR && ./run-cloudflared.sh"
    fi
    echo "✅ Started successfully."
}

stop_app() {
    echo "[+] Stopping the application..."
    if [ "$OS_TYPE" == "linux" ]; then
        systemctl stop telecloud telecloud-tunnel 2>/dev/null
    else
        tmux kill-session -t $SESSION 2>/dev/null
        pkill -f "\./telecloud" 2>/dev/null
        pkill -f "cloudflared tunnel run" 2>/dev/null
    fi
    echo "✅ Stopped everything."
}

restart_app() {
    stop_app
    start_app
}

manage_tunnel() {
    echo "1. Install / Reconfigure Cloudflare Tunnel"
    echo "2. Remove Cloudflare Tunnel"
    echo "3. Go back"
    read -p "Choose an option (1-3): " tc
    
    case $tc in
        1)
            if [ ! -f "$HOME/.cloudflared/cert.pem" ] && [ ! -f "/etc/cloudflared/cert.pem" ]; then
                cloudflared tunnel login
            fi
            if [ ! -f "$BASE_DIR/tunnel.txt" ]; then
                cloudflared tunnel create telecloud-tunnel > "$BASE_DIR/tunnel.txt"
            fi
            read -p "Enter the domain to route (e.g., telecloud.domain.com): " NEW_DOMAIN
            if [ ! -z "$NEW_DOMAIN" ]; then
                cloudflared tunnel route dns -f telecloud-tunnel "$NEW_DOMAIN"
                if [ $? -eq 0 ]; then
                    echo "$NEW_DOMAIN" > "$BASE_DIR/domain.txt"
                    echo "✅ DNS routed successfully! (Please restart the app to apply)"
                else
                    echo "❌ Error routing DNS."
                fi
            fi
            ;;
        2)
            echo "[+] Deleting Tunnel..."
            if [ "$OS_TYPE" == "linux" ]; then
                systemctl stop telecloud-tunnel 2>/dev/null
                systemctl disable telecloud-tunnel 2>/dev/null
            else  # termux and macos
                pkill -f "cloudflared tunnel run" 2>/dev/null
            fi
            cloudflared tunnel delete -f telecloud-tunnel 2>/dev/null
            rm -f "$BASE_DIR/tunnel.txt" "$BASE_DIR/domain.txt"
            echo "✅ Tunnel removed."
            echo "------------------------------------------------------"
            echo "📢 NOTE: Please visit dash.cloudflare.com to remove"
            echo "the DNS record for the old Tunnel if no longer used!"
            echo "------------------------------------------------------"
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
                journalctl -u telecloud.service -f -n 50
            else
                [ -f "$BASE_DIR/app.log" ] && tail -f -n 50 "$BASE_DIR/app.log" || echo "❌ No app log file found (make sure the app is running)."
            fi
            ;;
        2)
            if [ "$OS_TYPE" == "linux" ]; then
                journalctl -u telecloud-tunnel.service -f -n 50
            else
                [ -f "$BASE_DIR/tunnel.log" ] && tail -f -n 50 "$BASE_DIR/tunnel.log" || echo "❌ No tunnel log file found (make sure tunnel is running)."
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

update_app() {
    echo "[+] Checking for updates..."
    API_DATA=$(curl -s "https://api.github.com/repos/dabeecao/telecloud-go/releases/latest")
    LATEST=$(echo "$API_DATA" | jq -r ".tag_name")
    LOCAL=$(cat "$BASE_DIR/version.txt" 2>/dev/null)

    if [ "$LATEST" == "$LOCAL" ]; then
        echo "✅ You are on the latest version ($LOCAL)."
        return
    fi

    echo "🔥 New version available: $LATEST. Updating..."
    TARGET=$(uname -m)

    if [[ "$TARGET" == "aarch64" || "$TARGET" == "arm64" ]]; then
        TARGET="arm64"
    elif [[ "$TARGET" == "x86_64" ]]; then
        TARGET="amd64"
    elif [[ "$TARGET" == "armv7l" || "$TARGET" == "armhf" ]]; then
        TARGET="armv7"
    fi
    
    if [ "$TARGET" == "arm64" ]; then
        URL=$(echo "$API_DATA" | jq -r '.assets[] | select(.name | contains("linux_arm64")) | .browser_download_url')
    elif [ "$TARGET" == "amd64" ]; then
        URL=$(echo "$API_DATA" | jq -r '.assets[] | select(.name | contains("linux_amd64") or contains("linux_x86_64")) | .browser_download_url')
    elif [ "$TARGET" == "armv7" ]; then
        URL=$(echo "$API_DATA" | jq -r '.assets[] | select(.name | contains("linux_armv7")) | .browser_download_url')
    fi

    if [ -z "$URL" ] || [ "$URL" == "null" ]; then
        echo "❌ Error: Executable not found for architecture $TARGET."
        return
    fi

    echo "Downloading update..."
    wget -qO telecloud.tar.gz "$URL" || { echo "❌ Error downloading file!"; return; }
    
    stop_app
    tar -xvzf telecloud.tar.gz -C "$BASE_DIR" || { echo "❌ Error extracting file!"; return; }
    
    echo "$LATEST" > "$BASE_DIR/version.txt"
    rm telecloud.tar.gz
    echo "✅ Update completed. Please choose Restart."
}

update_setup_script() {
    echo "[+] Checking for management script updates..."
    local SCRIPT_URL="https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-setup-en.sh"
    # Download temporary file
    if wget -qO "$BASE_DIR/auto-setup-en.sh.new" "$SCRIPT_URL"; then
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
    echo "1. Initial Login (-auth)"
    echo "2. Reset Password (-resetpass)"
    echo "3. Update this Setup Script"
    echo "4. Return to Main Menu"
    read -p "Choose a command (1-4): " cmd_choice
    
    case $cmd_choice in
        1)
            echo "[+] Opening login interface..."
            cd "$BASE_DIR" && ./telecloud -auth
            ;;
        2)
            echo "[+] Resetting password..."
            cd "$BASE_DIR" && ./telecloud -resetpass
            ;;
        3)
            update_setup_script
            ;;
        *) return ;;
    esac
}

uninstall() {
    echo "⚠️ WARNING: You are about to completely remove the app and Tunnel."
    read -p "Confirm uninstallation? (y/n): " cf
    if [ "$cf" == "y" ]; then
        stop_app
        echo "[+] Deleting Tunnel on Cloudflare..."
        cloudflared tunnel delete -f telecloud-tunnel 2>/dev/null

        echo "------------------------------------------------------"
        echo "📢 IMPORTANT NOTE:"
        echo "The script has deleted the Tunnel, but the DNS records"
        echo "on your Cloudflare Dashboard still exist."
        echo "PLEASE REMEMBER to visit dash.cloudflare.com and"
        echo "delete the old DNS records to keep your setup clean."
        echo "------------------------------------------------------"
        
        if [ "$OS_TYPE" == "linux" ]; then
            systemctl disable telecloud telecloud-tunnel 2>/dev/null
            rm -f /etc/systemd/system/telecloud.service
            rm -f /etc/systemd/system/telecloud-tunnel.service
            systemctl daemon-reload
        fi
        
        rm -rf "$BASE_DIR" "$(command -v telecloud)"
        echo "✅ Uninstalled successfully. Script will exit."
        exit
    fi
}

while true; do
    clear
    echo "=========================================="
    echo "         TELECLOUD MANAGER MENU           "
    echo "=========================================="
    echo "  1. System Status"
    echo "  2. Start App"
    echo "  3. Stop App"
    echo "  4. Restart App"
    echo "  5. Manage Tunnel (Install/Route/Remove)"
    echo "  6. View Logs"
    echo "  7. Edit Config (.env)"
    echo "  8. Telecloud Commands (Auth / Reset Pass)"
    echo "  9. Check for Updates"
    echo "  10. Uninstall"
    echo "  11. Exit"
    echo "=========================================="
    read -p "Choose an option (1-11): " c
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
        10) uninstall ;;
        11) clear; exit ;;
        *) echo "[!] Invalid choice!"; pause ;;
    esac
done
EOF
    chmod +x "$BIN_DIR/telecloud"
}

# =============================
# MAIN EXECUTION BLOCK
# =============================
rollback() {
    echo -e "\n[!] INSTALLATION ERROR! Cleaning up..."
    rm -rf "$BASE_DIR" telecloud.tar.gz 2>/dev/null
    exit 1
}

# Command-line argument handling
if [ "$1" == "--update-menu" ]; then
    create_menu
    exit 0
fi

if [ ! -f "$BASE_DIR/telecloud" ]; then
    echo "--- FIRST TIME TELECLOUD INSTALLATION ---"
    trap rollback INT TERM
    install_dependencies || rollback
    download_telecloud || rollback
    create_env || rollback
    
    read -p "Do you want to set up Cloudflare Tunnel connection right now? (y/n): " setup_tnl
    if [ "$setup_tnl" == "y" ]; then
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
    echo "In the Menu, please select Option 8: 'Telecloud Commands' -> 'Initial Login' to set up!"
    echo "============================================="
    exit 0
fi

"$BIN_DIR/telecloud"
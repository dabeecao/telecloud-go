#!/bin/bash

# ==========================================
# 1. TỰ ĐỘNG NHẬN DIỆN MÔI TRƯỜNG & BIẾN
# ==========================================

# Hàm phát hiện package manager dựa vào /etc/os-release và lệnh có sẵn
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
        echo "[!] Không nhận diện được trình quản lý gói. Hỗ trợ: apt, dnf, yum, pacman, apk, zypper, brew."
        exit 1
    fi

    # Đọc tên distro để thông báo
    DISTRO_NAME="Linux"
    if [ "$(uname -s)" == "Darwin" ]; then
        DISTRO_NAME="macOS $(sw_vers -productVersion)"
    elif [ -f /etc/os-release ]; then
        DISTRO_NAME=$(. /etc/os-release && echo "${PRETTY_NAME:-$NAME}")
    fi
    echo "[+] Hệ điều hành: $DISTRO_NAME (Package manager: $PKG_MGR)"
}

# Hàm cài một gói, bỏ qua nếu đã có
pkg_install() {
    local pkg="$1"
    # Chuẩn hoá tên lệnh để kiểm tra (một số gói có tên lệnh khác tên gói)
    local cmd="${2:-$pkg}"
    if command -v "$cmd" &>/dev/null; then
        echo "[✓] $pkg đã được cài sẵn, bỏ qua."
        return 0
    fi
    echo "[+] Đang cài đặt $pkg..."
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
    echo "[+] Hệ điều hành: Termux (Android)"
elif [ "$(uname -s)" == "Darwin" ]; then
    OS_TYPE="macos"
    BASE_DIR="$HOME/telecloud-go"
    BIN_DIR="/usr/local/bin"
    if ! command -v brew &>/dev/null; then
        echo "[!] Homebrew chưa được cài đặt. Vui lòng cài trước:"
        echo "    /bin/bash -c \"\$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\""
        exit 1
    fi
    PKG_MGR="brew"
    echo "[+] Hệ điều hành: macOS $(sw_vers -productVersion) (Package manager: brew)"
else
    OS_TYPE="linux"
    BASE_DIR="/opt/telecloud-go"
    BIN_DIR="/usr/local/bin"

    if [ "$EUID" -ne 0 ]; then
        echo "[!] Môi trường Linux yêu cầu chạy bằng quyền root (sudo). Vui lòng thử lại!"
        exit 1
    fi

    detect_pkg_manager

    # Cập nhật danh sách gói (chỉ với apt)
    if [ "$PKG_MGR" == "apt" ]; then
        apt update -qq
    elif [ "$PKG_MGR" == "pacman" ]; then
        pacman -Sy --noconfirm
    fi
fi

SESSION="telecloud"

# ========================
# 2. CÀI ĐẶT PHỤ THUỘC
# ========================
install_dependencies() {
    echo "[+] Đang kiểm tra và cài đặt các gói cần thiết..."

    if [ "$OS_TYPE" == "linux" ]; then
        # Cài lần lượt, bỏ qua gói đã có
        for pkg in curl wget tar unzip jq tmux nano; do
            pkg_install "$pkg"
        done

        echo ""
        echo "[!] Lưu ý: FFmpeg chỉ dùng để tạo ảnh thu nhỏ (thumbnail) cho video/audio."
        echo "[!] Trên các dòng chip Exynos hoặc thiết bị yếu, FFmpeg có thể gây lỗi hoặc treo máy."
        read -p "[?] Bạn có muốn cài đặt FFmpeg không? (y/n): " install_ffmpeg
        [ "$install_ffmpeg" == "y" ] && pkg_install "ffmpeg"

        # Cài Cloudflared nếu chưa có
        if ! command -v cloudflared &>/dev/null; then
            echo "[+] Đang cài đặt Cloudflared..."
            if [ "$OS_TYPE" == "macos" ]; then
                brew install cloudflared || { echo "[!] Cài cloudflared qua brew thất bại!"; return 1; }
            else
                ARCH=$(uname -m)
                case "$ARCH" in
                    x86_64)          ARCH="amd64" ;;
                    aarch64|arm64)   ARCH="arm64" ;;
                    armv7l|armhf)    ARCH="armv7" ;;
                    *)
                        echo "[!] Kiến trúc không hỗ trợ để tải Cloudflared: $ARCH"
                        return 1
                    ;;
                esac
                CF_URL="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${ARCH}"
                # Fallback: thử wget trước, nếu không có dùng curl
                if command -v wget &>/dev/null; then
                    wget -qO /usr/local/bin/cloudflared "$CF_URL" || { echo "[!] Tải cloudflared thất bại!"; return 1; }
                elif command -v curl &>/dev/null; then
                    curl -fsSL "$CF_URL" -o /usr/local/bin/cloudflared || { echo "[!] Tải cloudflared thất bại!"; return 1; }
                else
                    echo "[!] Cần wget hoặc curl để tải Cloudflared!"; return 1
                fi
                chmod +x /usr/local/bin/cloudflared
            fi
            echo "[+] Cloudflared đã cài xong!"
        else
            echo "[✓] cloudflared đã được cài sẵn, bỏ qua."
        fi
    else
        # Termux
        echo ""
        echo "[!] Lưu ý: FFmpeg chỉ dùng để tạo ảnh thu nhỏ (thumbnail) cho video/audio."
        echo "[!] Trên các dòng chip Exynos hoặc thiết bị yếu, FFmpeg có thể gây lỗi hoặc treo máy."
        read -p "[?] Bạn có muốn cài đặt FFmpeg không? (y/n): " install_ffmpeg

        MAIN_PACKAGES="wget curl tar unzip tmux cloudflared jq nano"
        [ "$install_ffmpeg" == "y" ] && MAIN_PACKAGES="$MAIN_PACKAGES ffmpeg"

        for pkg in $MAIN_PACKAGES; do
            if ! command -v "$pkg" &>/dev/null; then
                echo "[+] Cài đặt $pkg..."
                pkg install -y "$pkg" || return 1
            else
                echo "[✓] $pkg đã được cài sẵn, bỏ qua."
            fi
        done
    fi
}

# =============================
# 3. TẢI VÀ LƯU BINARY
# =============================
download_telecloud() {
    echo "[+] Đang lấy thông tin phiên bản mới nhất từ GitHub..."
    API_DATA=$(curl -fsSL "https://api.github.com/repos/dabeecao/telecloud-go/releases/latest")
    
    VERSION=$(echo "$API_DATA" | jq -r ".tag_name")
    if [ -z "$VERSION" ] || [ "$VERSION" == "null" ]; then
        echo "[!] Không lấy được thông tin phiên bản từ GitHub!"; return 1
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
        echo "[!] Không tìm thấy binary cho kiến trúc $TARGET!"; return 1
    fi

    echo "[+] Đang tải phiên bản $VERSION..."
    wget -qO telecloud.tar.gz "$URL" || return 1
    mkdir -p "$BASE_DIR"
    tar -xzf telecloud.tar.gz -C "$BASE_DIR" || return 1

    if [ ! -f "$BASE_DIR/telecloud" ]; then
        echo "[!] Binary 'telecloud' không tìm thấy!"; return 1
    fi
    
    echo "$VERSION" > "$BASE_DIR/version.txt"
    rm telecloud.tar.gz
}

# =============================
# 4. CẤU HÌNH .ENV
# =============================
create_env() {
    if [ ! -f "$BASE_DIR/.env" ]; then
        echo "[+] Thiết lập cấu hình .env..."

        API_ID=""
        while [ -z "$API_ID" ]; do
            read -p "Nhập API_ID (Bắt buộc): " API_ID
        done

        API_HASH=""
        while [ -z "$API_HASH" ]; do
            read -p "Nhập API_HASH (Bắt buộc): " API_HASH
        done

        read -p "Cổng PORT [Mặc định 8091]: " PORT
        PORT=${PORT:-8091}

        read -p "LOG_GROUP_ID [Mặc định me]: " LOG_GROUP_ID
        LOG_GROUP_ID=${LOG_GROUP_ID:-me}

        read -p "MAX_UPLOAD_SIZE_MB [Mặc định 2000]: " MAX_UPLOAD
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
        echo "✅ Đã lưu cấu hình .env"
    fi
}

# =============================
# 5. CẤU HÌNH CLOUDFLARED
# =============================
cloudflared_setup() {
    if [ ! -f "$HOME/.cloudflared/cert.pem" ] && [ ! -f "/etc/cloudflared/cert.pem" ]; then
        echo "[!] Bạn cần đăng nhập Cloudflare..."
        cloudflared tunnel login || return 1
    fi

    if [ ! -f "$BASE_DIR/tunnel.txt" ]; then
        echo "[+] Đang tạo Cloudflare Tunnel..."
        cloudflared tunnel create telecloud-tunnel > "$BASE_DIR/tunnel.txt" || return 1
    fi

    read -p "Nhập tên miền của bạn (VD: telecloud.domain.com) hoặc Enter để bỏ qua: " MY_DOMAIN
    if [ ! -z "$MY_DOMAIN" ]; then
        echo "[+] Đang trỏ DNS (Force)..."
        cloudflared tunnel route dns -f telecloud-tunnel "$MY_DOMAIN" || echo "[!] Lỗi trỏ DNS. Có thể thiết lập lại trong Menu."
        echo "$MY_DOMAIN" > "$BASE_DIR/domain.txt"
        echo "✅ Đã trỏ DNS xong!"
    fi
}

# =============================
# 6. KHỞI TẠO DỊCH VỤ / SCRIPT CHẠY
# =============================
create_run_scripts() {
    local APP_PORT=$(grep PORT "$BASE_DIR/.env" | cut -d'=' -f2)
    APP_PORT=${APP_PORT:-8091}

    if [ "$OS_TYPE" == "linux" ]; then
        # Cấu hình Systemd cho Linux
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
        # Cấu hình Tmux cho Termux / macOS
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
# 7. TẠO MENU QUẢN LÝ
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
        echo "Vui lòng chạy lệnh bằng quyền root (sudo telecloud)."
        exit 1
    fi
fi

SESSION="telecloud"

pause() {
    echo ""
    read -p "Nhấn Enter để quay lại Menu..."
}

check_status() {
    echo "=========================================="
    echo "            TRẠNG THÁI HỆ THỐNG             "
    echo "=========================================="
    [ -f "$BASE_DIR/version.txt" ] && echo "📌 Phiên bản        : $(cat $BASE_DIR/version.txt)"
    
    if [ -f "$BASE_DIR/.env" ]; then
        APP_PORT=$(grep PORT "$BASE_DIR/.env" | cut -d'=' -f2)
        echo "📌 Cổng ứng dụng    : ${APP_PORT:-8091}"
    fi

    if [ "$OS_TYPE" == "linux" ]; then
        systemctl is-active --quiet telecloud && echo "✅ Telecloud App    : Running" || echo "❌ Telecloud App    : Stopped"
        systemctl is-active --quiet telecloud-tunnel && echo "✅ CF Tunnel        : Online" || echo "❌ CF Tunnel        : Offline"
    else
        # Termux and macOS both use tmux
        tmux has-session -t $SESSION 2>/dev/null && echo "✅ TMUX (Nền)       : Running" || echo "❌ TMUX (Nền)       : Stopped"
        pgrep -f "\./telecloud" > /dev/null && echo "✅ Telecloud App    : Running" || echo "❌ Telecloud App    : Stopped"
        pgrep -f "cloudflared tunnel run" > /dev/null && echo "✅ CF Tunnel        : Online" || echo "❌ CF Tunnel        : Offline"
    fi
    
    if [ -f "$BASE_DIR/domain.txt" ]; then
        echo "🔗 Tên miền         : https://$(cat $BASE_DIR/domain.txt)"
    else
        echo "🔗 Tên miền         : Chưa cấu hình"
    fi
    echo "=========================================="
}

start_app() {
    if [ ! -f "$BASE_DIR/session.json" ]; then
        echo "❌ LỖI: Bạn chưa đăng nhập Telegram!"
        echo "Vui lòng chọn Mục 8: 'Các lệnh của Telecloud' -> 'Đăng nhập lần đầu' trước."
        return 1
    fi

    echo "[+] Đang khởi động ứng dụng..."
    if [ "$OS_TYPE" == "linux" ]; then
        systemctl enable --now telecloud
        [ -f "$BASE_DIR/tunnel.txt" ] && systemctl enable --now telecloud-tunnel
    else
        tmux new-session -d -s $SESSION "cd $BASE_DIR && ./run.sh"
        [ -f "$BASE_DIR/tunnel.txt" ] && tmux split-window -h -t $SESSION "cd $BASE_DIR && ./run-cloudflared.sh"
    fi
    echo "✅ Đã khởi động."
}

stop_app() {
    echo "[+] Đang dừng ứng dụng..."
    if [ "$OS_TYPE" == "linux" ]; then
        systemctl stop telecloud telecloud-tunnel 2>/dev/null
    else
        tmux kill-session -t $SESSION 2>/dev/null
        pkill -f "\./telecloud" 2>/dev/null
        pkill -f "cloudflared tunnel run" 2>/dev/null
    fi
    echo "✅ Đã dừng toàn bộ."
}

restart_app() {
    stop_app
    start_app
}

manage_tunnel() {
    echo "1. Cài đặt / Cấu hình lại Cloudflare Tunnel"
    echo "2. Gỡ bỏ Cloudflare Tunnel"
    echo "3. Quay lại"
    read -p "Chọn chức năng (1-3): " tc
    
    case $tc in
        1)
            if [ ! -f "$HOME/.cloudflared/cert.pem" ] && [ ! -f "/etc/cloudflared/cert.pem" ]; then
                cloudflared tunnel login
            fi
            if [ ! -f "$BASE_DIR/tunnel.txt" ]; then
                cloudflared tunnel create telecloud-tunnel > "$BASE_DIR/tunnel.txt"
            fi
            read -p "Nhập tên miền muốn trỏ (VD: telecloud.domain.com): " NEW_DOMAIN
            if [ ! -z "$NEW_DOMAIN" ]; then
                cloudflared tunnel route dns -f telecloud-tunnel "$NEW_DOMAIN"
                if [ $? -eq 0 ]; then
                    echo "$NEW_DOMAIN" > "$BASE_DIR/domain.txt"
                    echo "✅ Đã trỏ DNS xong! (Hãy restart app để áp dụng)"
                else
                    echo "❌ Lỗi khi trỏ DNS."
                fi
            fi
            ;;
        2)
            echo "[+] Đang xoá Tunnel..."
            if [ "$OS_TYPE" == "linux" ]; then
                systemctl stop telecloud-tunnel 2>/dev/null
                systemctl disable telecloud-tunnel 2>/dev/null
            else  # termux and macos
                pkill -f "cloudflared tunnel run" 2>/dev/null
            fi
            cloudflared tunnel delete -f telecloud-tunnel 2>/dev/null
            rm -f "$BASE_DIR/tunnel.txt" "$BASE_DIR/domain.txt"
            echo "✅ Đã xoá Tunnel."
            echo "------------------------------------------------------"
            echo "📢 LƯU Ý: Vui lòng truy cập dash.cloudflare.com để"
            echo "xoá bản ghi DNS của Tunnel cũ nếu bạn không dùng nữa!"
            echo "------------------------------------------------------"
            ;;
        *) return ;;
    esac
}

view_logs() {
    echo "=========================================="
    echo "            XEM NHẬT KÝ (LOGS)            "
    echo "=========================================="
    echo "1. Xem Log Ứng dụng (Telecloud)"
    echo "2. Xem Log Cloudflare Tunnel"
    echo "3. Quay lại"
    read -p "Chọn log muốn xem (1-3): " log_choice

    if [[ "$log_choice" == "1" || "$log_choice" == "2" ]]; then
        echo "💡 MẸO: Nhấn Ctrl+C để thoát chế độ xem log."
        echo "Sau khi thoát, nếu menu bị tắt, hãy gõ lại lệnh 'telecloud' để mở lại."
        echo "Đang tải log..."
        sleep 2
    fi

    case $log_choice in
        1)
            if [ "$OS_TYPE" == "linux" ]; then
                journalctl -u telecloud.service -f -n 50
            else
                [ -f "$BASE_DIR/app.log" ] && tail -f -n 50 "$BASE_DIR/app.log" || echo "❌ Chưa có file log ứng dụng (hãy đảm bảo app đang chạy)."
            fi
            ;;
        2)
            if [ "$OS_TYPE" == "linux" ]; then
                journalctl -u telecloud-tunnel.service -f -n 50
            else
                [ -f "$BASE_DIR/tunnel.log" ] && tail -f -n 50 "$BASE_DIR/tunnel.log" || echo "❌ Chưa có file log tunnel (hãy đảm bảo tunnel đang chạy)."
            fi
            ;;
        *) return ;;
    esac
}

edit_env() {
    echo "=========================================="
    echo "          SỬA CẤU HÌNH (.ENV)             "
    echo "=========================================="
    if [ ! -f "$BASE_DIR/.env" ]; then
        echo "❌ Không tìm thấy file .env tại $BASE_DIR!"
        return
    fi

    if command -v nano >/dev/null 2>&1; then
        nano "$BASE_DIR/.env"
    elif command -v vi >/dev/null 2>&1; then
        vi "$BASE_DIR/.env"
    else
        echo "❌ Cần cài đặt nano hoặc vi để chỉnh sửa!"
        return
    fi

    echo "✅ Đã lưu cấu hình!"
    read -p "Bạn có muốn khởi động lại ứng dụng để áp dụng ngay không? (y/n): " rs
    if [ "$rs" == "y" ]; then
        stop_app
        start_app
    fi
}

update_app() {
    echo "[+] Đang kiểm tra bản cập nhật..."
    API_DATA=$(curl -s "https://api.github.com/repos/dabeecao/telecloud-go/releases/latest")
    LATEST=$(echo "$API_DATA" | jq -r ".tag_name")
    LOCAL=$(cat "$BASE_DIR/version.txt" 2>/dev/null)

    if [ "$LATEST" == "$LOCAL" ]; then
        echo "✅ Bạn đang ở bản mới nhất ($LOCAL)."
        return
    fi

    echo "🔥 Có bản mới: $LATEST. Đang tiến hành cập nhật..."
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
        echo "❌ Lỗi: Không tìm thấy file chạy cho kiến trúc $TARGET."
        return
    fi

    echo "Đang tải bản cập nhật..."
    wget -qO telecloud.tar.gz "$URL" || { echo "❌ Lỗi khi tải file!"; return; }
    
    stop_app
    tar -xvzf telecloud.tar.gz -C "$BASE_DIR" || { echo "❌ Lỗi khi giải nén!"; return; }
    
    echo "$LATEST" > "$BASE_DIR/version.txt"
    rm telecloud.tar.gz
    echo "✅ Đã cập nhật xong. Vui lòng chọn Khởi động lại."
}

update_setup_script() {
    echo "[+] Đang kiểm tra cập nhật cho script quản lý..."
    local SCRIPT_URL="https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-setup.sh"
    # Tải về file tạm
    if wget -qO "$BASE_DIR/auto-setup.sh.new" "$SCRIPT_URL"; then
        mv "$BASE_DIR/auto-setup.sh.new" "$BASE_DIR/auto-setup.sh"
        chmod +x "$BASE_DIR/auto-setup.sh"
        echo "✅ Đã cập nhật xong file auto-setup.sh."
        # Gọi chính file vừa tải để cập nhật BIN_DIR (hành động cài đè menu)
        bash "$BASE_DIR/auto-setup.sh" --update-menu
        echo "✅ Đã cập nhật xong menu lệnh 'telecloud'."
        echo "[!] Vui lòng thoát và gõ lại lệnh 'telecloud' để áp dụng thay đổi."
    else
        echo "❌ Lỗi: Không thể tải bản cập nhật từ GitHub."
    fi
}

telecloud_commands() {
    echo "=========================================="
    echo "          CÁC LỆNH CỦA TELECLOUD          "
    echo "=========================================="
    echo "1. Đăng nhập lần đầu (-auth)"
    echo "2. Reset mật khẩu (-resetpass)"
    echo "3. Cập nhật chính Script này (Setup Script)"
    echo "4. Quay lại Menu chính"
    read -p "Chọn lệnh (1-4): " cmd_choice
    
    case $cmd_choice in
        1)
            echo "[+] Đang mở giao diện đăng nhập..."
            cd "$BASE_DIR" && ./telecloud -auth
            ;;
        2)
            echo "[+] Đang tiến hành reset mật khẩu..."
            cd "$BASE_DIR" && ./telecloud -resetpass
            ;;
        3)
            update_setup_script
            ;;
        *) return ;;
    esac
}

uninstall() {
    echo "⚠️ CẢNH BÁO: Bạn sắp xoá sạch ứng dụng và Tunnel."
    read -p "Xác nhận gỡ cài đặt? (y/n): " cf
    if [ "$cf" == "y" ]; then
        stop_app
        echo "[+] Đang xoá Tunnel trên hệ thống Cloudflare..."
        cloudflared tunnel delete -f telecloud-tunnel 2>/dev/null

        echo "------------------------------------------------------"
        echo "📢 LƯU Ý QUAN TRỌNG:"
        echo "Script đã xoá Tunnel trên hệ thống, nhưng bản ghi DNS"
        echo "trên Dashboard Cloudflare vẫn còn tồn tại."
        echo "Bạn HÃY NHỚ truy cập dash.cloudflare.com để xoá"
        echo "bản ghi DNS cũ để tránh rác hệ thống."
        echo "------------------------------------------------------"
        
        if [ "$OS_TYPE" == "linux" ]; then
            systemctl disable telecloud telecloud-tunnel 2>/dev/null
            rm -f /etc/systemd/system/telecloud.service
            rm -f /etc/systemd/system/telecloud-tunnel.service
            systemctl daemon-reload
        fi
        
        rm -rf "$BASE_DIR" "$(command -v telecloud)"
        echo "✅ Đã gỡ bỏ sạch sẽ. Script sẽ thoát."
        exit
    fi
}

while true; do
    clear
    echo "=========================================="
    echo "         TELECLOUD MANAGER MENU           "
    echo "=========================================="
    echo "  1. Trạng thái hệ thống"
    echo "  2. Khởi động ứng dụng"
    echo "  3. Dừng ứng dụng"
    echo "  4. Khởi động lại ứng dụng"
    echo "  5. Quản lý Tunnel (Cài mới/Đổi miền/Gỡ)"
    echo "  6. Xem Log (Nhật ký hệ thống)"
    echo "  7. Sửa cấu hình (.env)"
    echo "  8. Các lệnh của Telecloud (Auth / Reset Pass)"
    echo "  9. Kiểm tra Cập nhật (Update)"
    echo "  10. Gỡ cài đặt ứng dụng"
    echo "  11. Thoát"
    echo "=========================================="
    read -p "Chọn chức năng (1-11): " c
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
        *) echo "[!] Lựa chọn không hợp lệ!"; pause ;;
    esac
done
EOF
    chmod +x "$BIN_DIR/telecloud"
}

# =============================
# KHỐI THỰC THI CHÍNH
# =============================
rollback() {
    echo -e "\n[!] LỖI CÀI ĐẶT! Đang dọn dẹp..."
    rm -rf "$BASE_DIR" telecloud.tar.gz 2>/dev/null
    exit 1
}

# Xử lý tham số dòng lệnh (nếu có)
if [ "$1" == "--update-menu" ]; then
    create_menu
    exit 0
fi

if [ ! -f "$BASE_DIR/telecloud" ]; then
    echo "--- CÀI ĐẶT TELECLOUD LẦN ĐẦU ---"
    trap rollback INT TERM
    install_dependencies || rollback
    download_telecloud || rollback
    create_env || rollback
    
    read -p "Bạn có muốn mở kết nối Cloudflare Tunnel ngay bây giờ không? (y/n): " setup_tnl
    if [ "$setup_tnl" == "y" ]; then
        cloudflared_setup || rollback
    fi
    
    create_run_scripts || rollback
    create_menu || rollback
    trap - INT TERM
    
    echo "============================================="
    echo "✅ CÀI ĐẶT THÀNH CÔNG!"
    echo "Gõ lệnh sau để mở Menu Quản lý:"
    echo "   telecloud"
    echo ""
    echo "Trong Menu, hãy chọn Mục 8: 'Các lệnh của Telecloud' -> 'Đăng nhập lần đầu' để thiết lập nhé!"
    echo "============================================="
    exit 0
fi

"$BIN_DIR/telecloud"
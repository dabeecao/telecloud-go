# 🛠️ Installation Guide / Hướng dẫn cài đặt

This guide covers the different ways to install TeleCloud on various platforms.
Hướng dẫn này bao gồm các cách khác nhau để cài đặt TeleCloud trên nhiều nền tảng.

---

## 🇻🇳 Tiếng Việt

### 1. Cài đặt tự động (Khuyên dùng)

Đây là cách đơn giản nhất để cài đặt, cấu hình và quản lý TeleCloud. Script sẽ tự động cài đặt các phụ thuộc (FFmpeg, Tmux, Cloudflared...), cấu hình dịch vụ và cung cấp menu quản lý.

#### Trên Windows
1. Tải tệp [**`auto-install.bat`**](https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-install.bat) về thư mục cài đặt.
2. Click chuột phải và chọn **Run as Administrator**.
3. Sử dụng Menu để:
    * Tự động cài đặt FFmpeg & Cloudflared.
    * Tải phiên bản TeleCloud mới nhất.
    * Cấu hình Cloudflare Tunnel (tên miền riêng).
    * Khởi động/Dừng ứng dụng chạy ngầm và xem log.

#### Trên Linux / Termux / macOS / Raspberry Pi
Script hỗ trợ Ubuntu, Debian, CentOS, Arch, macOS (Homebrew), Termux và ARM (Raspberry Pi).

```bash
# Sử dụng curl (Khuyên dùng)
curl -fsSL https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-setup.sh -o auto-setup.sh && bash auto-setup.sh

# Hoặc sử dụng wget
wget -qO auto-setup.sh https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-setup.sh && bash auto-setup.sh
```

**⚠️ Lưu ý khi dùng Termux**: Bạn nên tải Termux từ [GitHub Releases](https://github.com/termux/termux-app/releases) hoặc [F-Droid](https://f-droid.org/packages/com.termux/).

---

### 2. Cài đặt thủ công (Sử dụng Binary đã biên dịch)

#### Bước 1: Yêu cầu hệ thống
Bạn cần cài đặt **FFmpeg**, **yt-dlp** và **aria2** (tùy chọn) để sử dụng đầy đủ tính năng:
*   **Ubuntu/Debian**: `sudo apt install ffmpeg python3` và tải yt-dlp binary.
*   **Redhat-base**: `sudo yum install ffmpeg python3` thông qua RPM Fusion.
*   **Alpine Linux**: `apk add ffmpeg python3 yt-dlp aria2`
*   **Windows**: Tải bản build sẵn của FFmpeg, yt-dlp, aria2 và thêm vào PATH.

#### Bước 2: Tải về và Khởi động
1. Truy cập mục [**Releases**](https://github.com/dabeecao/telecloud-go/releases) và tải về phiên bản phù hợp.
2. Khởi động ứng dụng:
   ```bash
   ./telecloud # Linux/macOS
   telecloud.exe # Windows
   ```
3. Truy cập `http://localhost:8091/setup` để hoàn tất cấu hình qua giao diện Web Wizard.

---

## 🇺🇸 English

### 1. Automatic Installation (Recommended)

#### On Windows
1. Download [**`auto-install-en.bat`**](https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-install-en.bat).
2. Right-click and select **Run as Administrator**.
3. Use the Menu to install dependencies, latest release, and setup Cloudflare Tunnel.

#### On Linux / Termux / macOS / Raspberry Pi
Supports Ubuntu, Debian, CentOS, Arch, macOS, Termux, and ARM.

```bash
# Using curl
curl -fsSL https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-setup-en.sh -o auto-setup-en.sh && bash auto-setup-en.sh
```

---

### 2. Manual Installation (Using Prebuilt Binary)

#### Step 1: System Requirements
* **Ubuntu/Debian**: `sudo apt install ffmpeg python3`.
* **Redhat-based**: `sudo yum install ffmpeg python3` via RPM Fusion.
* **Alpine Linux**: `apk add ffmpeg python3 yt-dlp aria2`.
* **Windows**: Download FFmpeg, yt-dlp, and aria2 binaries and add to PATH.

#### Step 2: Download & Startup
1. Get the binary from [**Releases**](https://github.com/dabeecao/telecloud-go/releases).
2. Run the application and access `http://localhost:8091/setup` for the Web Setup Wizard.

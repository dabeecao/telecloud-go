# 🛠️ Installation Guide / Hướng dẫn cài đặt

This guide covers the different ways to install TeleCloud on various platforms.
Hướng dẫn này bao gồm các cách khác nhau để cài đặt TeleCloud trên nhiều nền tảng.

---

## 🇻🇳 Tiếng Việt

### 1. Cài đặt tự động (Khuyên dùng)

#### Trên Windows
1. Tải tệp [**`auto-install.bat`**](https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-install.bat) về thư mục bạn muốn cài đặt.
2. Click chuột phải và chọn **Run as Administrator**.
3. Sử dụng Menu để cài đặt FFmpeg, Cloudflared và TeleCloud.

#### Trên Linux / Termux / macOS / Raspberry Pi
Script hỗ trợ Ubuntu, Debian, CentOS, Arch, macOS (Homebrew), Termux và ARM (Raspberry Pi).

```bash
# Sử dụng curl
curl -fsSL https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-setup.sh -o auto-setup.sh && bash auto-setup.sh
```

### 2. Cài đặt thủ công (Sử dụng Binary)

1. **Yêu cầu hệ thống**: Cài đặt **FFmpeg**, **yt-dlp** và **aria2** (tùy chọn).
2. **Tải về**: Truy cập [Releases](https://github.com/dabeecao/telecloud-go/releases) và tải bản phù hợp.
3. **Khởi động**:
   ```bash
   ./telecloud # Linux/macOS
   telecloud.exe # Windows
   ```
4. **Thiết lập**: Truy cập `http://localhost:8091/setup` để hoàn tất cấu hình qua giao diện Web.

---

## 🇺🇸 English

### 1. Automatic Installation (Recommended)

#### On Windows
1. Download [**`auto-install-en.bat`**](https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-install-en.bat) to your installation folder.
2. Right-click and select **Run as Administrator**.
3. Use the Menu to install FFmpeg, Cloudflared, and TeleCloud.

#### On Linux / Termux / macOS / Raspberry Pi
Supports Ubuntu, Debian, CentOS, Arch, macOS (Homebrew), Termux, and ARM (Raspberry Pi).

```bash
# Using curl
curl -fsSL https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-setup-en.sh -o auto-setup-en.sh && bash auto-setup-en.sh
```

### 2. Manual Installation (Using Binary)

1. **System Requirements**: Install **FFmpeg**, **yt-dlp**, and **aria2** (optional).
2. **Download**: Visit [Releases](https://github.com/dabeecao/telecloud-go/releases) and download the appropriate version.
3. **Startup**:
   ```bash
   ./telecloud # Linux/macOS
   telecloud.exe # Windows
   ```
4. **Setup**: Access `http://localhost:8091/setup` to complete the configuration via the Web UI.

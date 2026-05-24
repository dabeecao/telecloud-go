# 🛠️ Development & Localization / Phát triển & Bản dịch

Guide for developers and contributors who want to build TeleCloud from source or contribute translations.
Hướng dẫn dành cho nhà phát triển và người đóng góp muốn tự build TeleCloud hoặc đóng góp bản dịch.

---

## 🇻🇳 Tiếng Việt

### 1. Build từ nguồn

#### Phương pháp 1: Build bằng Docker (Khuyên dùng)
Docker xử lý toàn bộ quá trình build mà không cần cài đặt Go hay Bun trên máy.
1. Clone dự án: `git clone --recursive https://github.com/dabeecao/telecloud-go.git`
2. Build image: `sudo docker build --build-arg DEFAULT_API_ID=your_api_id --build-arg DEFAULT_API_HASH=your_api_hash -t telecloud:local .`
3. Chạy image vừa build:
   ```bash
   sudo docker run -d -p 8091:8091 -v "$(pwd)/data:/app/data" --env-file .env telecloud:local
   ```

#### Phương pháp 2: Build thủ công (Native)
1. Cài đặt **Golang (1.26+)** và **Bun** (https://bun.com).
2. Clone với `--recursive` (Bắt buộc để lấy code frontend):
   `git clone --recursive https://github.com/dabeecao/telecloud-go.git`
3. Build Frontend:
   ```bash
   cd web
   bun install
   bun run build.js
   cd ..
   # Hoặc đơn giản là chạy `make frontend` ở thư mục gốc
   ```
4. Build Backend:
   ```bash
   go mod tidy
   go build -o telecloud
   ```

##### Cách nhúng API ID & API Hash mặc định khi build cục bộ:
Để tránh bị lộ thông tin API nhạy cảm khi đẩy code lên GitHub, TeleCloud hỗ trợ tự động nạp thông tin này từ file `.env` cục bộ (được bỏ qua bởi Git).
1. Khai báo `API_ID` và `API_HASH` trong tệp `.env`.
2. Sử dụng các công cụ build:
   - **Bằng Makefile**: Chạy lệnh `make` hoặc `make build`. Script sẽ tự động lấy API từ tệp `.env` để nhúng lúc biên dịch.
   - **Bằng Script build test**: Chạy lệnh `./build-test.sh`. Script cũng sẽ tự đọc `.env`, hoặc hiển thị hộp thoại yêu cầu nhập nếu không tìm thấy.
   - **Bằng lệnh thủ công**:
     ```bash
     go build -ldflags="-X telecloud/config.DefaultAPIIDStr=YOUR_API_ID -X telecloud/config.DefaultAPIHash=YOUR_API_HASH" -o telecloud
     ```


### 2. Đóng góp bản dịch (Localization)

Mã nguồn frontend nằm ở repo: [**dabeecao/telecloud-frontend**](https://github.com/dabeecao/telecloud-frontend).
1. Tìm tệp bản dịch trong `static/locales/` (VD: `vi.json`).
2. Tạo tệp mới (VD: `fr.json`) và dịch từ `en.json`.
3. Thêm ngôn ngữ vào `availableLangs` trong `static/js/common.js`.
4. Gửi Pull Request vào repository frontend.

---

## 🇺🇸 English

### 1. Build from Source

#### Method 1: Docker Build (Recommended)
1. `git clone --recursive https://github.com/dabeecao/telecloud-go.git`
2. `sudo docker build --build-arg DEFAULT_API_ID=your_api_id --build-arg DEFAULT_API_HASH=your_api_hash -t telecloud:local .`

#### Method 2: Manual Build
1. Install **Golang (1.26+)** and **Bun** (https://bun.com).
2. Build frontend in `web/` using `bun install && bun run build.js` (or `make frontend` in root).
3. Run `go mod tidy` and `go build -o telecloud` in root.

##### Injecting default API credentials during local build:
To prevent exposing sensitive API credentials in Git history, TeleCloud supports loading them dynamically from a local `.env` file (which is ignored by Git).
1. Define `API_ID` and `API_HASH` in your `.env` file.
2. Build using helper tools:
   - **Using Makefile**: Run `make` or `make build`. It will extract credentials from `.env` and embed them.
   - **Using Test Script**: Run `./build-test.sh`. It also reads from `.env` or prompts for inputs if missing.
   - **Manually**:
     ```bash
     go build -ldflags="-X telecloud/config.DefaultAPIIDStr=YOUR_API_ID -X telecloud/config.DefaultAPIHash=YOUR_API_HASH" -o telecloud
     ```


### 2. Contributing Translations
Frontend source: [**dabeecao/telecloud-frontend**](https://github.com/dabeecao/telecloud-frontend).
1. Edit JSON files in `static/locales/`.
2. Add the language to `availableLangs` in `static/js/common.js`.
3. Submit a Pull Request.

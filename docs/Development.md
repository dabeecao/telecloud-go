# 🛠️ Development & Localization / Phát triển & Bản dịch

Guide for developers and contributors who want to build TeleCloud from source or contribute translations.
Hướng dẫn dành cho nhà phát triển và người đóng góp muốn tự build TeleCloud hoặc đóng góp bản dịch.

---

## 🇻🇳 Tiếng Việt

### 1. Build từ nguồn

#### Phương pháp 1: Build bằng Docker (Khuyên dùng)
Docker xử lý toàn bộ quá trình build mà không cần cài đặt Go hay Node.js trên máy.
1. Clone dự án: `git clone --recursive https://github.com/dabeecao/telecloud-go.git`
2. Build image: `sudo docker build -t telecloud:local .`
3. Chạy image vừa build:
   ```bash
   sudo docker run -d -p 8091:8091 -v "$(pwd)/data:/app/data" --env-file .env telecloud:local
   ```

#### Phương pháp 2: Build thủ công (Native)
1. Cài đặt **Golang (1.24+)** và **Node.js**.
2. Clone với `--recursive` (Bắt buộc để lấy code frontend):
   `git clone --recursive https://github.com/dabeecao/telecloud-go.git`
3. Build Frontend:
   ```bash
   cd web
   npm install
   npm run build
   cd ..
   ```
4. Build Backend:
   ```bash
   go mod tidy
   go build -o telecloud
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
2. `sudo docker build -t telecloud:local .`

#### Method 2: Manual Build
1. Install **Golang (1.24+)** and **Node.js**.
2. Build frontend in `web/` using `npm install && npm run build`.
3. Run `go mod tidy` and `go build -o telecloud` in root.

### 2. Contributing Translations
Frontend source: [**dabeecao/telecloud-frontend**](https://github.com/dabeecao/telecloud-frontend).
1. Edit JSON files in `static/locales/`.
2. Add the language to `availableLangs` in `static/js/common.js`.
3. Submit a Pull Request.

# 🛠️ Development & Localization / Phát triển & Bản dịch

Guide for developers and contributors.
Hướng dẫn dành cho nhà phát triển và người đóng góp.

---

## 🇻🇳 Tiếng Việt

### 1. Build từ nguồn

#### Cách 1: Sử dụng Docker (Khuyên dùng)
```bash
git clone --recursive https://github.com/dabeecao/telecloud-go.git
cd telecloud-go
docker build -t telecloud:local .
```

#### Cách 2: Build thủ công
1. Cài đặt **Golang (1.24+)**.
2. Clone với `--recursive` để lấy code frontend.
3. Chạy `npm install` và `npm run build` trong thư mục `web/`.
4. Chạy `go build -o telecloud`.

### 2. Đóng góp bản dịch
Mã nguồn frontend nằm ở [dabeecao/telecloud-frontend](https://github.com/dabeecao/telecloud-frontend).
- Tệp bản dịch: `static/locales/*.json`.
- Gửi Pull Request vào repo frontend để đóng góp.

---

## 🇺🇸 English

### 1. Build from Source

#### Method 1: Using Docker (Recommended)
```bash
git clone --recursive https://github.com/dabeecao/telecloud-go.git
cd telecloud-go
docker build -t telecloud:local .
```

#### Method 2: Manual Build
1. Install **Golang (1.24+)**.
2. Clone with `--recursive` to fetch frontend code.
3. Run `npm install` and `npm run build` in the `web/` directory.
4. Run `go build -o telecloud`.

### 2. Contributing Translations
The frontend source code is at [dabeecao/telecloud-frontend](https://github.com/dabeecao/telecloud-frontend).
- Translation files: `static/locales/*.json`.
- Submit Pull Requests to the frontend repository to contribute.

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

### 3. Quy trình kiểm tra trước khi gửi Pull Request

Mọi Pull Request vào `main` đều được kiểm tra tự động bởi workflow [`.github/workflows/ci.yml`](../.github/workflows/ci.yml). Hãy chạy các bước tương tự ở máy cục bộ trước khi push để tránh CI thất bại:

```bash
make check      # gofmt-check + go vet + go test  (cổng kiểm tra đầy đủ)
```

Các lệnh chi tiết:

| Lệnh | Mô tả |
|---|---|
| `make fmt` | Định dạng toàn bộ mã Go bằng `gofmt -w` |
| `make fmt-check` | Chỉ kiểm tra định dạng, không ghi file (dùng trong CI) |
| `make vet` | Chạy `go vet` trên tất cả package |
| `make test` | Chạy toàn bộ unit test |
| `make test-race` | Chạy test kèm race detector (CI chạy trên Linux/Windows/macOS) |
| `make cover` | Xuất báo cáo độ phủ ra `coverage.out` |

**Yêu cầu bắt buộc:**

- Mã Go phải được định dạng bằng `gofmt` — CI sẽ **thất bại** nếu còn file chưa định dạng.
- `go vet ./...` phải sạch (không có cảnh báo).
- Không được giảm độ phủ test hiện có. Khi thêm logic mới (đặc biệt trong `database/`, `s3/`, `webdav/`), hãy thêm unit test đi kèm.
- Test phải chạy được trên cả Linux, Windows và macOS (tránh phụ thuộc đường dẫn tuyệt đối kiểu `/tmp` hay `C:\`, dùng `t.TempDir()`).

### 4. Lưu ý về phát triển Cơ sở dữ liệu (SQL) & Khóa mã hóa

#### Khóa mã hóa (`master.key`):
- Để tránh xung đột đường dẫn lưu trữ khóa `master.key` khi thư mục `data` được tạo sau lúc ứng dụng khởi động, toàn bộ mã nguồn sử dụng hàm `utils.GetMasterKeyFilePath()` để xác định vị trí tệp khóa chủ.
- Hàm này tự động phát hiện tệp khóa tồn tại sẵn tại các đường dẫn: `/app/data/master.key`, `data/master.key` hoặc gần file SQLite DB `DATABASE_PATH`.
- Trong phát triển, luôn gọi hàm `utils.GetMasterKeyFilePath()` thay vì tự nội suy đường dẫn để tránh lỗi mất khóa hay sinh khóa mới khi cấu hình MySQL/Postgres.

#### Quy tắc tương thích cơ sở dữ liệu:
- Khi viết các câu truy vấn SQL liên quan tới kiểu dữ liệu logic (`BOOLEAN`), ví dụ trường `is_folder` hoặc `force_password_change`:
  - **KHÔNG ĐƯỢC** gán hoặc so sánh trực tiếp với số nguyên (`1` hoặc `0`).
  - **BẮT BUỘC** sử dụng các từ khóa SQL tiêu chuẩn là `TRUE` và `FALSE` để đảm bảo tương thích tốt nhất trên cả SQLite, MySQL và đặc biệt là cơ chế so khớp kiểu dữ liệu nghiêm ngặt của PostgreSQL.

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

### 3. Pre-Pull-Request Checks

Every Pull Request targeting `main` is validated automatically by [`.github/workflows/ci.yml`](../.github/workflows/ci.yml). Run the same checks locally before pushing to avoid CI failures:

```bash
make check      # gofmt-check + go vet + go test  (the full gate)
```

Individual targets:

| Command | Description |
|---|---|
| `make fmt` | Format all Go source with `gofmt -w` |
| `make fmt-check` | Verify formatting without writing (what CI runs) |
| `make vet` | Run `go vet` across all packages |
| `make test` | Run the full unit test suite |
| `make test-race` | Run tests with the race detector (CI runs Linux/Windows/macOS) |
| `make cover` | Write a coverage profile to `coverage.out` |

**Requirements:**

- All Go files must be `gofmt`-formatted — CI **fails** on any unformatted file.
- `go vet ./...` must be clean (no findings).
- Do not lower existing test coverage. When adding logic — especially in `database/`, `s3/`, or `webdav/` — add accompanying unit tests.
- Tests must pass on Linux, Windows and macOS alike (avoid absolute paths such as `/tmp` or `C:\`; use `t.TempDir()` instead).

### 4. Database Development & Master Key Guidelines

#### Encryption Key (`master.key`):
- To prevent key path conflicts when the `data` directory is created dynamically during runtime, always use `utils.GetMasterKeyFilePath()` to resolve the persistent key path.
- The path resolver automatically checks standard locations (`/app/data/master.key`, `data/master.key`, or relative to SQLite `DATABASE_PATH`) before choosing a fallback write path.
- Never write ad-hoc file checks for `master.key`; use the unified helper instead to prevent auto-regeneration bugs under MySQL/PostgreSQL.

#### Database Compatibility Guidelines:
- When writing SQL queries that filter or write to `BOOLEAN` columns (such as `is_folder` or `force_password_change`):
  - **DO NOT** use integer literals (`1` or `0`).
  - **ALWAYS** use standard SQL boolean literals `TRUE` and `FALSE` to satisfy PostgreSQL's strict type verification while keeping full compatibility with SQLite and MySQL.

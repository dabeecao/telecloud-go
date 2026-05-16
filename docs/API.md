# 🔌 API Documentation / Tài liệu API

TeleCloud cung cấp hệ thống HTTP API mạnh mẽ để tích hợp vào các script, ứng dụng bên thứ ba hoặc CI/CD.

---

## 🇻🇳 Tiếng Việt

### 1. Thông tin chung
- **Base URL**: `http://your-domain.com/api/upload-api`
- **Xác thực**: Sử dụng Bearer Token trong Header.
  - Header: `Authorization: Bearer <YOUR_API_KEY>`
  - Lấy Key tại: **Cài đặt -> Upload API** trên giao diện Web.

---

### 2. Các Endpoint

#### A. Tải tệp lên (Upload)
Tải tệp trực tiếp từ máy cục bộ lên Telegram.
- **Endpoint**: `POST /upload`
- **Content-Type**: `multipart/form-data`
- **Tham số**:
  - `file`: (Bắt buộc) Tệp tin cần tải lên.
  - `path`: (Tùy chọn) Thư mục đích (Mặc định: `/`).
  - `share`: (Tùy chọn) Điền `public` để tự động tạo link chia sẻ sau khi tải xong.
  - `async`: (Tùy chọn) Điền `true` để tải lên trong nền (trả về `task_id`).

#### B. Tải từ URL từ xa (Remote Upload)
Tải tệp từ một đường dẫn URL (Hỗ trợ Direct Link, YouTube, TikTok...) về Telegram.
- **Endpoint**: `POST /remote`
- **Content-Type**: `application/json`
- **Tham số (JSON)**:
  - `url`: (Bắt buộc) Đường dẫn cần tải.
  - `path`: (Tùy chọn) Thư mục đích.
  - `async`: (Tùy chọn) Mặc định là `true` cho Remote Upload.

#### C. Tạo liên kết chia sẻ (Share Path)
Tạo link chia sẻ cho một tệp hoặc thư mục đã tồn tại.
- **Endpoint**: `POST /share`
- **Content-Type**: `application/json`
- **Tham số (JSON)**:
  - `path`: (Bắt buộc) Đường dẫn tệp/thư mục cần chia sẻ.

#### D. Kiểm tra trạng thái tác vụ (Task Status)
Kiểm tra tiến trình của các tác vụ chạy ngầm (async).
- **Endpoint**: `GET /tasks/<TASK_ID>`

#### E. Hủy tác vụ (Cancel Task)
Dừng và xóa một tác vụ đang chạy.
- **Endpoint**: `DELETE /tasks/<TASK_ID>`

---

### 3. Ví dụ lệnh cURL

**Tải lên cơ bản:**
```bash
curl -X POST http://localhost:8091/api/upload-api/upload \
  -H 'Authorization: Bearer YOUR_KEY' \
  -F 'file=@/path/to/file.zip' \
  -F 'path=/'
```

**Tải lên và lấy link chia sẻ ngay:**
```bash
curl -X POST http://localhost:8091/api/upload-api/upload \
  -H 'Authorization: Bearer YOUR_KEY' \
  -F 'file=@/path/to/file.zip' \
  -F 'share=public'
```

**Tải từ URL (Remote):**
```bash
curl -X POST http://localhost:8091/api/upload-api/remote \
  -H 'Authorization: Bearer YOUR_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"url": "https://example.com/video.mp4", "path": "/", "async": true}'
```

---

## 🇺🇸 English

### 1. General Information
- **Authentication**: `Authorization: Bearer <YOUR_API_KEY>`

### 2. Endpoints

- `POST /upload`: Upload local file to Telegram.
- `POST /remote`: Download from URL (YouTube, TikTok, Direct Link) to Telegram.
- `POST /share`: Create a share link for an existing path.
- `GET /tasks/<TASK_ID>`: Get async task progress.
- `DELETE /tasks/<TASK_ID>`: Cancel an active task.

### 3. cURL Examples

**Async Upload with Status Check:**
```bash
# 1. Start Async Upload
curl -X POST http://localhost:8091/api/upload-api/upload \
  -H 'Authorization: Bearer YOUR_KEY' \
  -F 'file=@/file.zip' \
  -F 'async=true'

# 2. Check Status
curl -H 'Authorization: Bearer YOUR_KEY' \
  http://localhost:8091/api/upload-api/tasks/<TASK_ID>
```

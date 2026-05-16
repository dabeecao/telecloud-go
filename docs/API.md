# 🔌 API Documentation / Tài liệu API

TeleCloud provides a simple HTTP API for remote uploads.
TeleCloud cung cấp một HTTP API đơn giản để tải tệp lên từ xa.

---

## 🇻🇳 Tiếng Việt

### Upload API
- **Endpoint**: `POST /api/upload-api/upload`
- **Xác thực**: Bearer Token (Lấy trong phần Cài đặt giao diện Web).
- **Tham số**:
  - `file`: (multipart/form-data) Tệp cần tải lên.
  - `path`: (Tùy chọn) Thư mục đích.
  - `share`: (Tùy chọn) Đặt "public" để lấy link chia sẻ ngay.

Chi tiết xem tại mục **Cài đặt -> Upload API** trên giao diện web.

---

## 🇺🇸 English

### Upload API
- **Endpoint**: `POST /api/upload-api/upload`
- **Authentication**: Bearer Token (Available in Web UI Settings).
- **Parameters**:
  - `file`: (multipart/form-data) File to upload.
  - `path`: (Optional) Destination path.
  - `share`: (Optional) Set to "public" to get a share link immediately.

Detailed documentation and examples are available in **Settings -> Upload API** section of the web interface.

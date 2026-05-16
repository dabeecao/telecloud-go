# ⚙️ Configuration Guide / Hướng dẫn cấu hình

Detailed information about configuring TeleCloud via environment variables and reverse proxies.
Thông tin chi tiết về việc cấu hình TeleCloud qua biến môi trường và reverse proxy.

---

## 🇻🇳 Tiếng Việt

### 1. Tệp .env (Biến môi trường)

Sao chép tệp `env.example` thành `.env` trong thư mục chứa file thực thi và điền các thông tin của bạn:

*   `API_ID` & `API_HASH`: (Tùy chọn) Lấy tại [my.telegram.org](https://my.telegram.org). Nếu để trống, bạn có thể thiết lập qua giao diện Web Setup.
*   `LOG_GROUP_ID`: (Tùy chọn) ID nhóm/kênh lưu file hoặc điền `me`. Nếu để trống, bạn có thể thiết lập qua giao diện Web Setup.
*   `PORT`: Cổng muốn chạy ứng dụng (mặc định: 8091).
*   `TG_UPLOAD_THREADS`: (Tùy chọn) Số luồng upload đồng thời cho mỗi file part. Mặc định là `2`. Có thể tăng lên `4` nếu mạng mạnh.
*   `BOT_TOKENS`: (Tùy chọn) Danh sách các token của Bot phụ, phân cách bằng dấu phẩy (VD: `token1,token2`). Các bot này sẽ giúp chia sẻ tải trọng với tài khoản chính, tăng tốc độ download/upload đáng kể.
    *   **Lưu ý**: Các bot phải được thêm vào nhóm/kênh lưu trữ (`LOG_GROUP_ID`) và được cấp quyền gửi tin nhắn. Nếu `LOG_GROUP_ID=me` (Saved Messages), tính năng Multi-bot sẽ tự động bị tắt.
*   `DATABASE_DRIVER`: (Tùy chọn) Loại cơ sở dữ liệu (`sqlite`, `mysql` hoặc `postgres`). Mặc định là `sqlite`.
*   `DATABASE_PATH`: (Tùy chọn) Đường dẫn tới file database nếu dùng SQLite (mặc định: `database.db`).
*   `DATABASE_DSN`: (Bắt buộc nếu dùng MySQL/Postgres) Chuỗi kết nối.
    *   VD MySQL: `user:pass@tcp(127.0.0.1:3306)/telecloud?parseTime=true&charset=utf8mb4`
    *   VD Postgres: `postgres://user:pass@127.0.0.1:5432/telecloud?sslmode=disable`
*   `THUMBS_DIR`: (Tùy chọn) Đường dẫn tới thư mục chứa ảnh thumbnail (mặc định: `./static/thumbs`).
*   `TEMP_DIR`: (Tùy chọn) Đường dẫn thư mục tạm dùng để chứa các mảnh file (chunks) (mặc định: `./temp`).
*   `PROXY_URL`: (Tùy chọn) Proxy để kết nối MTProto, hỗ trợ HTTP và SOCKS5 (VD: `socks5://127.0.0.1:1080`).
*   `FFMPEG_PATH`: Đường dẫn tới FFmpeg. Đặt thành `disabled` để tắt tính năng tạo ảnh thu nhỏ.
*   `YTDLP_PATH`: Đường dẫn tới yt-dlp. Đặt thành `disabled` để tắt tính năng tải từ URL.
*   `TORRENT_PATH`: Đường dẫn tới aria2c. Hệ thống tự động bật Torrent nếu tìm thấy. Đặt thành `disabled` để tắt.

**Lưu ý về Thứ tự ưu tiên**: Nếu bạn điền các thông số trong tệp `.env`, hệ thống sẽ **ưu tiên** sử dụng chúng và bỏ qua cấu hình trong cơ sở dữ liệu.

### 2. Cấu hình Nginx (Reverse Proxy)

Sử dụng mẫu cấu hình tối ưu sau:

```nginx
server {
    listen 80;
    server_name your.domain.com;

    # Quan trọng: Cho phép upload file lớn không giới hạn
    client_max_body_size 0;

    location / {
        proxy_pass http://127.0.0.1:8091;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Hỗ trợ Range requests cho streaming
        proxy_set_header Range $http_range;
        proxy_set_header If-Range $http_if_range;

        # Tắt buffering để hỗ trợ upload file lớn và streaming mượt hơn
        proxy_request_buffering off;
        proxy_buffering off;

        proxy_read_timeout 3600s;
    }

    # Hỗ trợ WebSockets
    location /api/ws {
        proxy_pass http://127.0.0.1:8091/api/ws;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 3600s;
    }
}
```

---

## 🇺🇸 English

### 1. .env File (Environment Variables)

Copy `env.example` to `.env` in the binary directory and fill in your details:

*   `API_ID` & `API_HASH`: (Optional) Get from [my.telegram.org](https://my.telegram.org). If empty, you can configure via Web Setup.
*   `LOG_GROUP_ID`: (Optional) ID of storage group or `me`. If empty, you can configure via Web Setup.
*   `PORT`: Application port (default: 8091).
*   `TG_UPLOAD_THREADS`: (Optional) Concurrent upload threads per part. Default: `2`.
*   `BOT_TOKENS`: (Optional) List of secondary Bot tokens (comma-separated). Helps increase performance significantly.
    *   **Note**: Bots must be added to the `LOG_GROUP_ID` and granted message permissions. Multi-bot is disabled if `LOG_GROUP_ID=me`.
*   `DATABASE_DRIVER`: `sqlite`, `mysql`, or `postgres`. Default: `sqlite`.
*   `DATABASE_DSN`: Required for MySQL/Postgres.
    *   Example MySQL: `user:pass@tcp(127.0.0.1:3306)/telecloud?parseTime=true&charset=utf8mb4`
    *   Example Postgres: `postgres://user:pass@127.0.0.1:5432/telecloud?sslmode=disable`
*   `THUMBS_DIR`: Directory for thumbnails (default: `./static/thumbs`).
*   `TEMP_DIR`: Path for temporary file chunks (default: `./temp`).
*   `PROXY_URL`: MTProto proxy, supports HTTP and SOCKS5.
*   `FFMPEG_PATH`: Path to FFmpeg. Set to `disabled` to skip thumbnails.
*   `YTDLP_PATH`: Path to yt-dlp. Set to `disabled` to skip URL downloads.
*   `TORRENT_PATH`: Path to aria2c. Set to `disabled` to disable Torrent support.

**Priority Note**: Variables in `.env` **override** any settings in the database.

### 2. Nginx Configuration (Reverse Proxy)

Optimized template for streaming and large uploads:

```nginx
server {
    listen 80;
    server_name your.domain.com;
    client_max_body_size 0;

    location / {
        proxy_pass http://127.0.0.1:8091;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }

    location /api/ws {
        proxy_pass http://127.0.0.1:8091/api/ws;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

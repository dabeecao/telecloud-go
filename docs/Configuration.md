# ⚙️ Configuration Guide / Hướng dẫn cấu hình

Detailed information about configuring TeleCloud via environment variables and reverse proxies.
Thông tin chi tiết về việc cấu hình TeleCloud qua biến môi trường và reverse proxy.

---

## 🇻🇳 Tiếng Việt

### 1. Tệp .env (Biến môi trường)

Sao chép tệp `env.example` thành `.env` trong thư mục chứa file thực thi và điền các thông tin của bạn:

*   `API_ID` & `API_HASH`: **Mặc định đã được nhúng sẵn trong tất cả các bản phát hành Binary và Docker Image chính thức**. Người dùng thông thường **KHÔNG CẦN CẤU HÌNH** và có thể đăng nhập ngay lập tức. Tùy chọn này chỉ dành cho Nhà phát triển hoặc Người dùng nâng cao muốn tự biên dịch hoặc sử dụng thông tin API riêng (cấu hình qua phần *Cài đặt nâng cao* trên Web Setup hoặc dùng `-ldflags` khi build). Không còn hỗ trợ khai báo qua tệp `.env`.
*   `LOG_GROUP_ID`: (Tùy chọn) ID nhóm/kênh lưu file hoặc điền `me`. Nếu để trống, bạn có thể thiết lập qua giao diện Web Setup.
    *   **Cách lấy LOG_GROUP_ID**: Tạo một nhóm Telegram mới, nhớ bật cho phép hiển thị lịch sử trong cài đặt nhóm, sử dụng bot `@get_all_telegram_id_bot`, thêm bot này vào nhóm và gửi lệnh `/getid`. ID nhóm sẽ được hiển thị dưới dạng `-100xxxxxxxxxx`, trong đó phần `-100xxx` là LOG_GROUP_ID. Hoặc bạn có thể để mặc định `me` để lưu vào phần tin nhắn đã lưu (nhưng lưu kiểu này sẽ làm rối hết các tin đã lưu của bạn).
*   `PORT`: Cổng muốn chạy ứng dụng (mặc định: 8091).
*   `TG_UPLOAD_THREADS`: (Tùy chọn) Số luồng upload đồng thời cho mỗi file part. Mặc định là `2`. Có thể tăng lên `4` nếu mạng mạnh.
*   `BOT_TOKENS`: **Không còn hỗ trợ cấu hình qua tệp .env**. Thay vào đó, bạn có thể dễ dàng quản lý và thêm các Bot phụ (Bot Pool) một cách trực quan trong phần *Cấu hình Bot Pool* ở trang Cài đặt của Admin trên giao diện Web, giúp phân phối tải trọng và tăng tốc độ tối đa.
*   `DATABASE_DRIVER`: (Tùy chọn) Loại cơ sở dữ liệu (`sqlite`, `mysql` hoặc `postgres`). Mặc định là `sqlite`.
*   `DATABASE_PATH`: (Tùy chọn) Đường dẫn tới file database nếu dùng SQLite (mặc định: `database.db`).
*   `DATABASE_DSN`: (Bắt buộc nếu dùng MySQL/Postgres) Chuỗi kết nối.
    *   VD MySQL: `user:pass@tcp(127.0.0.1:3306)/telecloud?parseTime=true&charset=utf8mb4`
    *   VD Postgres: `postgres://user:pass@127.0.0.1:5432/telecloud?sslmode=disable`
*   `TELECLOUD_MASTER_KEY`: (Tùy chọn) Khóa 32-byte dùng để mã hóa session và settings nhạy cảm. Nếu để trống, hệ thống sẽ tự động sinh và lưu trữ tại tệp `master.key` trong thư mục dữ liệu. **Cực kỳ quan trọng, hãy sao lưu tách biệt với DB.**
*   `LISTEN_ADDR`: (Tùy chọn) Địa chỉ IP lắng nghe của ứng dụng. Mặc định là `127.0.0.1` khi chưa thiết lập admin (để bảo mật trình thiết lập setup ban đầu), và tự động chuyển thành `0.0.0.0` sau khi hoàn tất thiết lập. Bạn có thể tự đặt địa chỉ IP cụ thể (ví dụ: `0.0.0.0` để mở cổng ra ngoài hoặc đặt sau Cloudflare Tunnel, Nginx, Tailscale).
*   `THUMBS_DIR`: (Tùy chọn) Đường dẫn tới thư mục chứa ảnh thumbnail (mặc định: `./static/thumbs`).
*   `TEMP_DIR`: (Tùy chọn) Đường dẫn thư mục tạm dùng để chứa các mảnh file (chunks) (mặc định: `./temp`).
*   `PROXY_URL`: (Tùy chọn) Proxy để kết nối MTProto, hỗ trợ HTTP và SOCKS5 (VD: `socks5://127.0.0.1:1080`).
*   `FFMPEG_PATH`: Đường dẫn tới FFmpeg. Đặt thành `disabled` để tắt tính năng tạo ảnh thu nhỏ.
*   `YTDLP_PATH`: Đường dẫn tới yt-dlp. Đặt thành `disabled` để tắt tính năng tải từ URL.
*   `TORRENT_PATH`: Đường dẫn tới aria2c. Hệ thống tự động bật Torrent nếu tìm thấy. Đặt thành `disabled` để tắt.
*   `S3_CORS_ALLOWED_ORIGINS`: (Tùy chọn) Danh sách tên miền được phép truy cập CORS vào S3 API, phân tách bằng dấu phẩy (VD: `https://app.example.com,http://localhost:3000`). Nếu để trống hoặc đặt là `*` (hoặc `0.0.0.0`), hệ thống sẽ cho phép mọi nguồn (Origin) truy cập qua trình duyệt.


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

*   `API_ID` & `API_HASH`: **Already embedded by default in all official Binary and Docker Image releases**. General users **DO NOT NEED TO CONFIGURE** these and can log in immediately. These options are strictly for Developers or Advanced Users who wish to compile from source or use their own custom API credentials (via the *Advanced settings* section in Web Setup or via `-ldflags` during build). No longer supported in the `.env` file.
*   `LOG_GROUP_ID`: (Optional) ID of storage group or `me`. If empty, you can configure via Web Setup.
    *   **How to get LOG_GROUP_ID**: Create a new Telegram group, make sure to enable "Chat History" in the group settings, add bot `@get_all_telegram_id_bot` to the group and send `/getid`. The group ID will be displayed in the format `-100xxxxxxxxxx`, which is your LOG_GROUP_ID. Or keep it as `me` (will clutter your Saved Messages).
*   `PORT`: Application port (default: 8091).
*   `TG_UPLOAD_THREADS`: (Optional) Concurrent upload threads per part. Default: `2`.
*   `BOT_TOKENS`: **No longer supported via the .env file**. Instead, you can easily configure and manage secondary bots (Bot Pool) dynamically and securely via the *Bot Pool Settings* section in the Admin Settings dashboard within the Web UI to maximize speeds.
*   `DATABASE_DRIVER`: `sqlite`, `mysql`, or `postgres`. Default: `sqlite`.
*   `DATABASE_DSN`: Required for MySQL/Postgres.
    *   Example MySQL: `user:pass@tcp(127.0.0.1:3306)/telecloud?parseTime=true&charset=utf8mb4`
    *   Example Postgres: `postgres://user:pass@127.0.0.1:5432/telecloud?sslmode=disable`
*   `TELECLOUD_MASTER_KEY`: (Optional) 32-byte master key used to encrypt sessions and sensitive settings. If empty, automatically generated and saved to `master.key` in your data directory. **Extremely important, back it up separately from the database.**
*   `LISTEN_ADDR`: (Optional) The IP address the application binds to. Defaults to `127.0.0.1` before setup is complete (to secure the initial setup wizard), and `0.0.0.0` after setup is complete. You can explicitly set this (e.g., `0.0.0.0` to expose the application port directly, or place it behind Cloudflare Tunnel, Nginx, or Tailscale).
*   `THUMBS_DIR`: Directory for thumbnails (default: `./static/thumbs`).
*   `TEMP_DIR`: Path for temporary file chunks (default: `./temp`).
*   `PROXY_URL`: MTProto proxy, supports HTTP and SOCKS5.
*   `FFMPEG_PATH`: Path to FFmpeg. Set to `disabled` to skip thumbnails.
*   `YTDLP_PATH`: Path to yt-dlp. Set to `disabled` to skip URL downloads.
*   `TORRENT_PATH`: Path to aria2c. Set to `disabled` to disable Torrent support.
*   `S3_CORS_ALLOWED_ORIGINS`: (Optional) Comma-separated list of origins allowed to access the S3 API via CORS (e.g., `https://app.example.com,http://localhost:3000`). If left blank or set to `*` (or `0.0.0.0`), all origins are allowed.


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

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
*   `TG_DOWNLOAD_PREFETCH`: (Tùy chọn) Số chunk 1MB được tải trước song song khi stream/tải xuống. Mặc định là `4`, tối đa `16`. Giảm xuống `2` nếu Bot Pool ít mà nhiều người xem cùng lúc gây lỗi `FLOOD_WAIT`; tăng chỉ có tác dụng khi Bot Pool đủ lớn.
*   `BOT_TOKENS`: **Không còn hỗ trợ cấu hình qua tệp .env**. Thay vào đó, bạn có thể dễ dàng quản lý và thêm các Bot phụ (Bot Pool) một cách trực quan trong phần *Cấu hình Bot Pool* ở trang Cài đặt của Admin trên giao diện Web, giúp phân phối tải trọng và tăng tốc độ tối đa.
*   `DATABASE_DRIVER`: (Tùy chọn) Loại cơ sở dữ liệu (`sqlite`, `mysql` hoặc `postgres`). Mặc định là `sqlite`.
*   `DATABASE_PATH`: (Tùy chọn) Đường dẫn tới file database nếu dùng SQLite (mặc định: `database.db`).
*   `DATABASE_DSN`: (Bắt buộc nếu dùng MySQL/Postgres) Chuỗi kết nối.
    *   VD MySQL: `user:pass@tcp(127.0.0.1:3306)/telecloud?parseTime=true&charset=utf8mb4`
    *   VD Postgres: `postgres://user:pass@127.0.0.1:5432/telecloud?sslmode=disable`
*   `TELECLOUD_MASTER_KEY`: (Tùy chọn) Khóa 32-byte dùng để mã hóa session và settings nhạy cảm. Nếu để trống, hệ thống sẽ tự động sinh và lưu trữ tại tệp `master.key` trong thư mục dữ liệu. **Cực kỳ quan trọng, hãy sao lưu tách biệt với DB.**
*   `LISTEN_ADDR`: (Tùy chọn) Địa chỉ IP lắng nghe của ứng dụng. Mặc định là `0.0.0.0` (lắng nghe trên mọi giao diện mạng để thuận tiện cài đặt từ xa). Bạn có thể tự đặt địa chỉ IP cụ thể (ví dụ: `127.0.0.1` để chỉ cho phép kết nối nội bộ hoặc đặt sau Cloudflare Tunnel, Nginx, Tailscale).
*   `THUMBS_DIR`: (Tùy chọn) Đường dẫn tới thư mục chứa ảnh thumbnail (mặc định: `./static/thumbs`).
*   `TEMP_DIR`: (Tùy chọn) Đường dẫn thư mục tạm dùng để chứa các mảnh file (chunks) (mặc định: `./temp`).
*   `PROXY_URL`: (Tùy chọn) Proxy để kết nối MTProto, hỗ trợ HTTP và SOCKS5 (VD: `socks5://127.0.0.1:1080`).
*   `FFMPEG_PATH`: Đường dẫn tới FFmpeg. Đặt thành `disabled` để tắt tính năng tạo ảnh thu nhỏ.
*   `YTDLP_PATH`: Đường dẫn tới yt-dlp. Đặt thành `disabled` để tắt tính năng tải từ URL.
*   `TORRENT_PATH`: Đường dẫn tới aria2c. Hệ thống tự động bật Torrent nếu tìm thấy. Đặt thành `disabled` để tắt.
*   `S3_CORS_ALLOWED_ORIGINS`: (Tùy chọn) Danh sách tên miền được phép truy cập CORS vào S3 API, phân tách bằng dấu phẩy (VD: `https://app.example.com,http://localhost:3000`). Nếu để trống hoặc đặt là `*` (hoặc `0.0.0.0`), hệ thống sẽ cho phép mọi nguồn (Origin) truy cập qua trình duyệt.


**Lưu ý về Thứ tự ưu tiên**: Nếu bạn điền các thông số trong tệp `.env`, hệ thống sẽ **ưu tiên** sử dụng chúng và bỏ qua cấu hình trong cơ sở dữ liệu.

### 2. Tinh chỉnh `TG_DOWNLOAD_PREFETCH`

Khi phát video hoặc tải file, TeleCloud tải trước (read-ahead) một số chunk 1MB nằm phía sau con trỏ đọc, thay vì chờ đọc xong chunk này mới xin chunk kế tiếp. `TG_DOWNLOAD_PREFETCH` quyết định số chunk đó.

Giá trị này ảnh hưởng tới **mọi** đường tải xuống: streaming trên Web, link chia sẻ, trình đọc CBZ/EPUB, **WebDAV** và **S3 API**.

**Bộ nhớ tiêu thụ**

Bộ đệm tải trước có trần cứng, **không phình theo kích thước file** (mỗi chunk được giải phóng ngay khi đọc xong). Với giá trị mặc định `4`:

| Thành phần | Bộ nhớ |
| :--- | :--- |
| Mỗi luồng tải | ~4 MB (`TG_DOWNLOAD_PREFETCH` × 1MB) |
| Mỗi luồng, lúc chuyển giữa 2 part | ~8 MB (trong thời gian ngắn) |
| Bộ đệm chunk dùng chung toàn hệ thống | 128 MB (cố định) |
| Tổng lượng tải trước đang chạy toàn hệ thống | tối đa 32 MB |

Ví dụ 40 người xem cùng lúc tiêu tốn khoảng 300–450 MB RAM.

**Nên đặt bao nhiêu**

| Tình huống | Giá trị |
| :--- | :--- |
| Mặc định, phù hợp đa số | `4` |
| VPS RAM thấp (≤ 1GB) hoặc nhiều người xem cùng lúc | `2` |
| Log xuất hiện nhiều lỗi `FLOOD_WAIT` | `2` |
| Bot Pool lớn (5+ bot) và mạng khỏe | `8` |

Tăng giá trị **chỉ có tác dụng khi Bot Pool đủ lớn**: các yêu cầu tải trước được chia đều cho các bot, nên nếu chỉ có một tài khoản thì tăng lên sẽ gây `FLOOD_WAIT` chứ không nhanh hơn. Hệ thống giới hạn tối đa `16`.

> **Lưu ý**: Khi dùng Bot Pool, file lớn được cắt thành nhiều part 500MB, nên mức ~8 MB mỗi luồng ở trên là trường hợp thường gặp với video dài.

### 3. Cấu hình Nginx (Reverse Proxy)

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

    # Hỗ trợ S3 API (Xác thực chữ ký được tối giản hóa để đạt độ tương thích 100%)
    # Hỗ trợ mọi ứng dụng khách S3 (Rclone, Cyberduck, Infuse, v.v.) qua mọi Proxy/Cloudflare.
    location /s3 {
        # Khuyên dùng $http_host để truyền Host gốc chính xác
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Tắt bộ đệm để truyền file dung lượng lớn qua S3 ổn định hơn
        proxy_request_buffering off;
        proxy_buffering off;
        client_max_body_size 0;

        # Cực kỳ quan trọng: KHÔNG THÊM dấu gạch chéo '/' ở cuối proxy_pass!
        # Việc thêm dấu gạch chéo cuối sẽ kích hoạt tính năng chuẩn hóa URI của Nginx,
        # làm giải mã ký tự đặc biệt %2F và cắt bỏ tiền tố /s3 dẫn tới sai chữ ký.
        proxy_pass http://127.0.0.1:8091;
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
*   `TG_DOWNLOAD_PREFETCH`: (Optional) Number of 1MB chunks prefetched in parallel while streaming or downloading. Default: `4`, capped at `16`. Lower it to `2` if a small Bot Pool serving many concurrent viewers triggers `FLOOD_WAIT`; raising it only helps when the Bot Pool is large enough.
*   `BOT_TOKENS`: **No longer supported via the .env file**. Instead, you can easily configure and manage secondary bots (Bot Pool) dynamically and securely via the *Bot Pool Settings* section in the Admin Settings dashboard within the Web UI to maximize speeds.
*   `DATABASE_DRIVER`: `sqlite`, `mysql`, or `postgres`. Default: `sqlite`.
*   `DATABASE_DSN`: Required for MySQL/Postgres.
    *   Example MySQL: `user:pass@tcp(127.0.0.1:3306)/telecloud?parseTime=true&charset=utf8mb4`
    *   Example Postgres: `postgres://user:pass@127.0.0.1:5432/telecloud?sslmode=disable`
*   `TELECLOUD_MASTER_KEY`: (Optional) 32-byte master key used to encrypt sessions and sensitive settings. If empty, automatically generated and saved to `master.key` in your data directory. **Extremely important, back it up separately from the database.**
*   `LISTEN_ADDR`: (Optional) The IP address the application binds to. Defaults to `0.0.0.0` (binds to all interfaces for remote setup accessibility). You can explicitly set this (e.g., `127.0.0.1` to restrict access to localhost only or place it behind Cloudflare Tunnel, Nginx, or Tailscale).
*   `THUMBS_DIR`: Directory for thumbnails (default: `./static/thumbs`).
*   `TEMP_DIR`: Path for temporary file chunks (default: `./temp`).
*   `PROXY_URL`: MTProto proxy, supports HTTP and SOCKS5.
*   `FFMPEG_PATH`: Path to FFmpeg. Set to `disabled` to skip thumbnails.
*   `YTDLP_PATH`: Path to yt-dlp. Set to `disabled` to skip URL downloads.
*   `TORRENT_PATH`: Path to aria2c. Set to `disabled` to disable Torrent support.
*   `S3_CORS_ALLOWED_ORIGINS`: (Optional) Comma-separated list of origins allowed to access the S3 API via CORS (e.g., `https://app.example.com,http://localhost:3000`). If left blank or set to `*` (or `0.0.0.0`), all origins are allowed.


**Priority Note**: Variables in `.env` **override** any settings in the database.

### Chunked upload

<a id="chunked-upload"></a>Resumable uploads through proxies with a request-size limit (Cloudflare Tunnel). Protocol: [ChunkedUploads.md](ChunkedUploads.md).

| Variable | Default | Description |
|---|---|---|
| `CHUNKED_UPLOAD_ENABLED` | `true` | Set `false` to disable the `/api/uploads` API. Existing uploads are unaffected. |
| `CHUNKED_UPLOAD_THRESHOLD_MB` | `80` | Files above this size should use the chunked protocol (advertised to clients). |
| `CHUNKED_UPLOAD_CHUNK_SIZE_MB` | `32` | Ceiling for the request body size (default 32). Clamped to 1–50 MiB (hard maximum). Clients auto-size below it from a speed probe; see [ChunkedUploads.md](ChunkedUploads.md#choosing-the-chunk-size-auto-sizing). |
| `CHUNKED_UPLOAD_MAX_PARALLEL` | `4` | Concurrent chunk requests per session (max 16). |
| `CHUNKED_UPLOAD_MAX_INFLIGHT` | `8` | Concurrent chunk requests for the whole server (max 64). Excess requests get `429` + `Retry-After`. |
| `CHUNKED_UPLOAD_STAGED_PARTS` | `2` | Telegram parts staged on disk per session. Disk use ≤ this × part size. |
| `CHUNKED_UPLOAD_PART_MB` | `0` | Optionally lower the Telegram storage part size (0 = account limit). |
| `CHUNKED_UPLOAD_MAX_RETRIES` | `6` | Telegram upload attempts per part per round (exponential backoff + jitter; flood-wait honoured). |
| `CHUNKED_UPLOAD_SESSION_TTL_HOURS` | `48` | Idle time after which an unfinished session is discarded. |
| `CHUNKED_UPLOAD_DIR` | `<TEMP_DIR>/../upload_sessions` | Staging directory. Must be writable and **not** inside `TEMP_DIR`. |

### 2. Tuning `TG_DOWNLOAD_PREFETCH`

When streaming video or downloading a file, TeleCloud reads ahead a number of 1MB chunks past the read cursor instead of waiting for the current chunk to finish before requesting the next one. `TG_DOWNLOAD_PREFETCH` controls how many.

It affects **every** download path: Web streaming, share links, the CBZ/EPUB readers, **WebDAV**, and the **S3 API**.

**Memory usage**

The read-ahead buffer is hard-capped and **does not grow with file size** (each chunk is released as soon as it is consumed). At the default of `4`:

| Component | Memory |
| :--- | :--- |
| Per download stream | ~4 MB (`TG_DOWNLOAD_PREFETCH` × 1MB) |
| Per stream, while crossing a part boundary | ~8 MB (briefly) |
| Shared server-wide chunk cache | 128 MB (fixed) |
| Total in-flight read-ahead, server-wide | 32 MB max |

As an example, 40 concurrent viewers use roughly 300–450 MB of RAM.

**Recommended values**

| Situation | Value |
| :--- | :--- |
| Default, fine for most setups | `4` |
| Low-RAM VPS (≤ 1GB) or many concurrent viewers | `2` |
| Logs show frequent `FLOOD_WAIT` errors | `2` |
| Large Bot Pool (5+ bots) on a fast connection | `8` |

Raising the value **only helps when the Bot Pool is large enough**: read-ahead requests are spread across bots, so with a single account a higher value causes `FLOOD_WAIT` rather than extra speed. The system caps it at `16`.

> **Note**: With a Bot Pool, large files are split into 500MB parts, so the ~8 MB per-stream figure above is the common case for long videos.

### 3. Nginx Configuration (Reverse Proxy)

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

    # S3 API Support (Signature verification relaxed for 100% client compatibility)
    # Supports all S3 clients (Rclone, Cyberduck, Infuse, etc.) seamlessly behind any Proxy/Cloudflare.
    location /s3 {
        # Recommended to use $http_host to pass the correct host header
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Disable buffering to allow stable large S3 uploads
        proxy_request_buffering off;
        proxy_buffering off;
        client_max_body_size 0;

        # Crucial: DO NOT add a trailing slash '/' at the end of proxy_pass!
        # A trailing slash forces Nginx to decode/normalize URI path (e.g. decodes %2F to /)
        # and strips the '/s3' prefix, which breaks S3 signature verification.
        proxy_pass http://127.0.0.1:8091;
    }
}
```

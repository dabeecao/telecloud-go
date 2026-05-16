# ⚙️ Configuration Guide / Hướng dẫn cấu hình

Detailed information about configuring TeleCloud via environment variables and reverse proxies.
Thông tin chi tiết về việc cấu hình TeleCloud qua biến môi trường và reverse proxy.

---

## 🇻🇳 Tiếng Việt

### 1. Tệp .env
Sao chép `env.example` thành `.env` và chỉnh sửa các tham số sau:

* `API_ID` & `API_HASH`: Lấy tại [my.telegram.org](https://my.telegram.org).
* `LOG_GROUP_ID`: ID nhóm/kênh lưu file hoặc điền `me`.
* `PORT`: Cổng chạy ứng dụng (mặc định 8091).
* `DATABASE_DRIVER`: `sqlite`, `mysql` hoặc `postgres`.
* `DATABASE_DSN`: Chuỗi kết nối cho MySQL/Postgres.
* `BOT_TOKENS`: Danh sách token Bot phụ (tăng tốc độ).
* `PROXY_URL`: Proxy kết nối (HTTP/SOCKS5).

### 2. Cấu hình Nginx (Reverse Proxy)
Sử dụng mẫu sau để hỗ trợ upload file lớn và streaming:

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

---

## 🇺🇸 English

### 1. .env File
Copy `env.example` to `.env` and edit the following parameters:

* `API_ID` & `API_HASH`: Get from [my.telegram.org](https://my.telegram.org).
* `LOG_GROUP_ID`: ID of the storage group/channel or `me`.
* `PORT`: Application port (default 8091).
* `DATABASE_DRIVER`: `sqlite`, `mysql`, or `postgres`.
* `DATABASE_DSN`: Connection string for MySQL/Postgres.
* `BOT_TOKENS`: List of secondary Bot tokens for load balancing.
* `PROXY_URL`: Connection proxy (HTTP/SOCKS5).

### 2. Nginx Configuration (Reverse Proxy)
Use the following template for large file uploads and streaming:

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

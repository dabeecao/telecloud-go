# 🐳 Docker Deployment / Triển khai với Docker

TeleCloud Docker image includes FFmpeg and yt-dlp built-in.
Image Docker của TeleCloud đã tích hợp sẵn FFmpeg và yt-dlp.

---

## 🇻🇳 Tiếng Việt

### 1. Sử dụng Docker Run (Nhanh)
```bash
docker run -d \
    --name telecloud \
    --restart unless-stopped \
    -p 8091:8091 \
    -v "$(pwd)/data:/app/data" \
    --env-file .env \
    ghcr.io/dabeecao/telecloud-go
```

### 2. Sử dụng Docker Compose (Khuyên dùng)
Tải `docker-compose.yml` và `.env` mẫu, sau đó chạy:
```bash
sudo docker compose up -d
```

Toàn bộ dữ liệu (database, thumbnails, temp) được lưu trong thư mục `./data/`.

---

## 🇺🇸 English

### 1. Using Docker Run (Quick)
```bash
docker run -d \
    --name telecloud \
    --restart unless-stopped \
    -p 8091:8091 \
    -v "$(pwd)/data:/app/data" \
    --env-file .env \
    ghcr.io/dabeecao/telecloud-go
```

### 2. Using Docker Compose (Recommended)
Download `docker-compose.yml` and `env.example` (rename to `.env`), then run:
```bash
sudo docker compose up -d
```

All data (database, thumbnails, temp) is stored in the `./data/` directory.

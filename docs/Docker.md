# 🐳 Docker Deployment / Triển khai với Docker

This is the recommended method for servers. The Docker image (Alpine-based) includes **FFmpeg and yt-dlp built-in**.
Đây là cách triển khai được khuyến nghị cho máy chủ. Image Docker (Alpine) đã tích hợp sẵn **FFmpeg và yt-dlp**.

---

## 🇻🇳 Tiếng Việt

### Phương pháp 1: Chạy bằng lệnh Docker (Nhanh)

1. Tải image: `docker pull ghcr.io/dabeecao/telecloud-go`
2. Cấu hình thư mục và `.env`:
   ```bash
   mkdir telecloud && cd telecloud
   curl -O https://raw.githubusercontent.com/dabeecao/telecloud-go/main/env.example
   mv env.example .env
   # Điền API_ID, API_HASH, LOG_GROUP_ID vào .env
   ```
3. Khởi động:
   ```bash
   mkdir -p data && sudo chmod 777 data
   sudo docker run -d \
       --name telecloud \
       --restart unless-stopped \
       -p 8091:8091 \
       -v "$(pwd)/data:/app/data" \
       --env-file .env \
       -e DATABASE_PATH=/app/data/database.db \
       -e THUMBS_DIR=/app/data/thumbs \
       -e TEMP_DIR=/app/data/temp \
       --user 65532:65532 \
       ghcr.io/dabeecao/telecloud-go
   ```

### Phương pháp 2: Docker Compose (Khuyên dùng)

1. Tải file cấu hình:
   ```bash
   mkdir telecloud && cd telecloud
   curl -O https://raw.githubusercontent.com/dabeecao/telecloud-go/main/docker-compose.yml
   curl -O https://raw.githubusercontent.com/dabeecao/telecloud-go/main/env.example
   mv env.example .env
   ```
2. Điền thông tin vào `.env` (API_ID, API_HASH...).
3. Khởi động: `sudo docker compose up -d`

#### Các lệnh quản lý:
*   Xem log: `sudo docker compose logs -f`
*   Dừng: `sudo docker compose stop`
*   Cập nhật: `sudo docker compose pull && sudo docker compose up -d`
*   Xóa container: `sudo docker compose down`

---

## 🇺🇸 English

### Method 1: Single Container (Quick)
```bash
# Prepare directory and run
mkdir -p data && sudo chmod 777 data
sudo docker run -d \
    --name telecloud \
    --restart unless-stopped \
    -p 8091:8091 \
    -v "$(pwd)/data:/app/data" \
    --env-file .env \
    -e DATABASE_PATH=/app/data/database.db \
    -e THUMBS_DIR=/app/data/thumbs \
    -e TEMP_DIR=/app/data/temp \
    --user 65532:65532 \
    ghcr.io/dabeecao/telecloud-go
```

### Method 2: Docker Compose (Recommended)
1. Download `docker-compose.yml` and `.env`.
2. Fill in the required fields in `.env`.
3. Run: `sudo docker compose up -d`

#### Useful Commands:
* `sudo docker compose logs -f`
* `sudo docker compose pull && sudo docker compose up -d`

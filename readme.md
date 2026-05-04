# TeleCloud

<div align="center">

🇻🇳 Tiếng Việt | [🇺🇸 English](./readme_en.md)

**[📢 Nhóm Beta Test](https://t.me/+p-d0qfGRbX4wNzJl)**
*Tham gia để test tính năng mới và báo cáo lỗi*

</div>

**TeleCloud** là một dự án cho phép sử dụng chính dung lượng lưu trữ gần như vô tận của Telegram để lưu trữ và quản lý tệp.

Dự án này đã được **viết lại hoàn toàn bằng Golang** từ dự án gốc [dabeecao/tele-cloud](https://github.com/dabeecao/tele-cloud) , đem lại hiệu năng xuất sắc, sử dụng bộ nhớ cực thấp và khả năng biên dịch thành một file thực thi (binary) duy nhất có thể chạy ở bất kỳ đâu mà không cần cài đặt môi trường phát triển.

---

## 📸 Ảnh xem trước giao diện

### 🖥️ Giao diện Máy tính
| | |
| :---: | :---: |
| <img src="preview/preview.jpg" width="100%"> | <img src="preview/preview-2.jpg" width="100%"> |
| <img src="preview/preview-3.jpg" width="100%"> | <img src="preview/preview-4.jpg" width="100%"> |

### 📱 Giao diện Điện thoại
| | | | | |
| :---: | :---: | :---: | :---: | :---: |
| <img src="preview/preview-5.jpg" width="100%"> | <img src="preview/preview-6.jpg" width="100%"> | <img src="preview/preview-7.jpg" width="100%"> | <img src="preview/preview-8.jpg" width="100%"> | <img src="preview/preview-9.jpg" width="100%"> |

> *Giao diện được thiết kế tối ưu hóa cho mọi thiết bị (Responsive Design)*

## ✨ Tính năng

* 📁 Lưu trữ file trực tiếp trên Telegram **không giới hạn dung lượng** (Tự động chia nhỏ file siêu lớn thành các mảnh 2GB/4GB).
* 🎬 Phát video và nhạc trực tiếp trong trang quản lý và liên kết chia sẻ (Hỗ trợ phát mượt mà các file đã chia nhỏ).
* 🔗 Liên kết chia sẻ có thể chọn liên kết thường hoặc link tải trực tiếp (Direct Link), hỗ trợ chia sẻ cả **Thư mục**.
* 🗂️ Giao diện quản lý (File Browser) trực quan, hỗ trợ chế độ xem **Lưới (Grid)** và **Danh sách (List)**.
* ⬆️ Upload song song (Multi-threading) tốc độ cao
* 📦 Upload chia nhỏ (chunk) để tối ưu tốc độ và ổn định
* 📂 Hỗ trợ **WebDAV**: Gắn TeleCloud thành ổ đĩa mạng trên máy tính (Windows, macOS, Linux).
* 🔌 **Upload API**: Cho phép upload file từ xa qua HTTP API (Bearer Token) để tích hợp vào script hoặc CI/CD.
* 📥 **Tải từ URL**: Hỗ trợ tải tệp trực tiếp từ đường dẫn URL về bộ lưu trữ.
* 🎥 **Tải đa phương tiện**: Hỗ trợ tải Video, Nhạc từ các nền tảng (YouTube, TikTok, Facebook...) bằng **yt-dlp** ngay trong giao diện.
* ⚡ **Tải trong nền**: Hỗ trợ tải tệp từ URL trong nền, không cần treo trình duyệt, có thông báo tiến trình real-time.
* 👥 **Quản lý đa người dùng**: Hỗ trợ tạo tài khoản con với không gian lưu trữ riêng biệt (Virtual Path).
* 🔐 **Passkey**: Hỗ trợ đăng nhập bảo mật bằng vân tay, khuôn mặt hoặc khóa bảo mật (WebAuthn).
* 🌐 **Đa ngôn ngữ**: Hỗ trợ tiếng Việt và tiếng Anh ở giao diện sử dụng

---

> [!NOTE]
> **Từ phiên bản 2.12.0 trở đi**, TeleCloud đã hỗ trợ cơ chế Cache Busting tự động cho các tệp tĩnh. Bạn không còn cần phải "Purge Cache" (Xóa cache) trên Cloudflare hay trình duyệt mỗi khi cập nhật phiên bản mới nữa.

---


## 🛠️ Cài đặt tự động

### Cài đặt tự động trên Windows

Dễ dàng cài đặt và quản lý TeleCloud trên Windows thông qua script tự động:

1. Tải tệp [**`auto-install.bat`**](https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-install.bat) về thư mục bạn muốn cài đặt.
2. Click chuột phải và chọn **Run as Administrator** (Chạy với quyền quản trị).
3. Sử dụng Menu để:
    * Tự động cài đặt FFmpeg & Cloudflared.
    * Tải phiên bản TeleCloud mới nhất từ GitHub.
    * Cấu hình Cloudflare Tunnel (tên miền riêng) cực kỳ đơn giản.
    * Khởi động/Dừng ứng dụng chạy ngầm và xem log trực tiếp.
  
### Cài đặt tự động trên (Linux / Termux / macOS / Raspberry Pi)

Đây là cách đơn giản và tự động nhất để cài đặt, cấu hình và quản lý TeleCloud. Script hỗ trợ tốt trên nhiều môi trường như Ubuntu, Debian, CentOS, Arch, macOS (Homebrew), Termux và các dòng chip ARM (Raspberry Pi).

Script sẽ tự động cài đặt các phụ thuộc (FFmpeg, Tmux, Cloudflared...), cấu hình dịch vụ và cung cấp menu quản lý tiện lợi qua lệnh `telecloud`.

**Cách sử dụng:**
```bash
# Sử dụng curl (Khuyên dùng)
curl -fsSL https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-setup.sh -o auto-setup.sh && bash auto-setup.sh
```

```bash
# Hoặc sử dụng wget
wget -qO auto-setup.sh https://raw.githubusercontent.com/dabeecao/telecloud-go/main/auto-setup.sh && bash auto-setup.sh
```
Hoặc nếu bạn đã tải mã nguồn về:
```bash
chmod +x auto-setup.sh
./auto-setup.sh
```

#### ⚠️ Lưu ý khi dùng Termux

Về Termux bạn nên tải nó từ một trong hai nguồn sau:

- [GitHub Releases (khuyên dùng)](https://github.com/termux/termux-app/releases)
- [F-Droid](https://f-droid.org/packages/com.termux/)

---

## 🚀 Hướng dẫn cài đặt nhanh (Sử dụng Binary đã biên dịch sẵn)

Đây là cách nhanh nhất để chạy TeleCloud mà không cần cài đặt môi trường lập trình.

### 1. Yêu cầu hệ thống
Bạn cần cài đặt **FFmpeg** và **yt-dlp** để hệ thống có thể tạo ảnh thu nhỏ (thumbnail) cho video/âm thanh và tải xuống tệp phương tiện từ URL.

*   **Ubuntu/Debian:** `sudo apt install ffmpeg python3` và tải file binary của yt-dlp.
*   **Redhat-base:** `sudo yum install ffmpeg python3` thông qua [Fedora and Red Hat Enterprise Linux packages](https://rpmfusion.org/)
*   **Alpine Linux:** `apk add ffmpeg python3 yt-dlp`
*   **Windows:** Tải bản build sẵn tại [ffmpeg.org](https://ffmpeg.org/download.html) và [yt-dlp](https://github.com/yt-dlp/yt-dlp/releases) rồi thêm vào PATH.

Nếu bạn không cài FFmpeg hoặc yt-dlp, dự án vẫn có thể hoạt động nhưng tính năng tạo ảnh thu nhỏ và tải tệp phương tiện từ URL sẽ không hoạt động.

### 2. Tải về TeleCloud
Truy cập mục [**Releases**](https://github.com/dabeecao/telecloud-go/releases) và tải về phiên bản phù hợp với hệ điều hành của bạn (Linux, Windows, hoặc macOS).

### 3. Cấu hình môi trường
Trong thư mục chứa file binary, bạn sẽ thấy tệp [`env.example`](.env.example). Hãy sao chép nó thành `.env` và điền các thông tin của bạn:

```bash
cp env.example .env
```

Nội dung chính trong tệp `.env`:
*   `API_ID` & `API_HASH`: Lấy tại [my.telegram.org](https://my.telegram.org).
*   `LOG_GROUP_ID`: ID nhóm/kênh lưu file hoặc điền `me` để lưu vào Saved Messages.
*   `PORT`: Cổng muốn chạy ứng dụng.
*   `TG_UPLOAD_THREADS`: (Tùy chọn) Số luồng upload đồng thời cho mỗi file part. Mặc định là `2`. Có thể tăng lên `4` nếu mạng mạnh.
*   `DATABASE_PATH`: (Tùy chọn) Đường dẫn tới file database (mặc định: `database.db`).
*   `THUMBS_DIR`: (Tùy chọn) Đường dẫn tới thư mục chứa ảnh thumbnail (mặc định: `./static/thumbs`).
*   `TEMP_DIR`: (Tùy chọn) Đường dẫn thư mục tạm dùng để chứa các mảnh file (chunks) trong quá trình tải lên (mặc định: `./temp`).
*   `PROXY_URL`: (Tùy chọn) Proxy để kết nối MTProto, hỗ trợ HTTP và SOCKS5 (VD: `socks5://127.0.0.1:1080`).
*   `FFMPEG_PATH`: (Tùy chọn) Đường dẫn tới file FFmpeg (mặc định: `ffmpeg`). Đặt thành "disabled" để bỏ qua hình thu nhỏ video/âm thanh nếu FFmpeg không được cài đặt hoặc gây ra lỗi.
*   `YTDLP_PATH`: (Tùy chọn) Đường dẫn tới yt-dlp (mặc định: `yt-dlp`). Đặt thành "disabled" để bỏ qua chức năng tải tệp phương tiện nếu yt-dlp không được cài đặt.

*   **Lưu ý về Theme (Giao diện)**: Ứng dụng hỗ trợ nhiều theme giao diện khác nhau (Neon, Cyberpunk, Lavender, Forest) cũng như chế độ hệ thống (System). Việc cấu hình Theme được thực hiện trực tiếp trong phần Cài đặt của Giao diện Web sau khi đăng nhập và không yêu cầu bất kỳ biến môi trường nào.
 
#### 🔑 Lấy API_ID và API_HASH

* Truy cập: https://my.telegram.org
* Đăng nhập bằng số điện thoại Telegram
* Chọn **API development tools**
* Tạo app mới
* Lấy:

   * `API_ID`
   * `API_HASH`

#### 📡 Lấy LOG_GROUP_ID

* Tạo nhóm Telegram rồi thêm Userbot vào hoặc nếu dùng chính tài khoản đó của bạn thì chỉ cần đơn giản tạo nhóm có một mình bạn. Bạn nhớ trong cài đặt nhóm phải đặt hiện lịch sử tin nhắn
* Mở bot [@get_all_tetegram_id_bot](https://t.me/get_all_telegram_id_bot) và thêm vào nhóm, sau khi thêm bot ở nhóm hãy gõ ```/getid```

* Bot sẽ phản hồi dạng:
```
🔹 CURRENT SESSION / PHIÊN HIỆN TẠI

• User ID / ID Người dùng: 36xxxxxxxx
• Chat ID / ID Trò chuyện: -100xxxxxxxxxx
• Message ID / ID Tin nhắn: x
• Chat Type / Loại hội thoại: supergroup
```

Thì lúc này ```Chat ID / ID Trò chuyện``` chính là LOG_GROUP_ID cần lấy và sẽ có dạng:

```
-100xxxxxxxxxx
```

### 4. Đăng nhập và Chạy
Mở terminal tại thư mục chứa file binary và thực hiện các bước sau:

**Bước A: Xác thực tài khoản (Chỉ thực hiện lần đầu)**
```bash
# Linux/macOS
./telecloud -auth

# Windows
telecloud.exe -auth
```
*Nhập số điện thoại, mã OTP và mật khẩu 2FA (nếu có) theo hướng dẫn.*

**Bước B: Khởi động máy chủ**
```bash
./telecloud
```

Truy cập giao diện web tại: `http://localhost:8091`
- **Lần đầu tiên truy cập**, hệ thống sẽ yêu cầu bạn tạo tài khoản và mật khẩu quản trị (Admin).
- Các cấu hình khác như đổi mật khẩu hay cấu hình **WebDAV** đều có thể được thực hiện trực tiếp trong phần **Cài đặt** của giao diện Web sau khi đăng nhập.
WebDAV tại: `http://localhost:8091/webdav`

## 🌐 Cấu hình Reverse Proxy (Nginx)

Nếu bạn muốn sử dụng Nginx làm Reverse Proxy (để dùng tên miền riêng, HTTPS), hãy sử dụng mẫu cấu hình tối ưu sau để hỗ trợ upload file lớn và streaming:

```nginx
server {
    listen 80;
    server_name your.domain.com;

    # Quan trọng: Cho phép upload file lớn không giới hạn
    client_max_body_size 0;

    location / {
        proxy_pass http://127.0.0.1:8091;

        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Hỗ trợ Range requests cho streaming (seek)
        proxy_set_header Range $http_range;
        proxy_set_header If-Range $http_if_range;

        # Tránh lỗi Connection khi proxy
        proxy_set_header Connection "";

        # QUAN TRỌNG: Tắt buffering để hỗ trợ upload file lớn và streaming mượt hơn
        proxy_request_buffering off;
        proxy_buffering off;

        # Tăng timeout để tránh đứt kết nối khi xử lý file lớn hoặc stream dài
        proxy_read_timeout 3600s;
        proxy_connect_timeout 3600s;
        proxy_send_timeout 3600s;
        send_timeout 3600s;
    }

    # Hỗ trợ WebSockets cho tính năng thông báo tiến trình real-time
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

## 🔌 Upload API

TeleCloud cung cấp một HTTP API đơn giản để bạn có thể tải tệp lên từ các script bên ngoài hoặc dòng lệnh.

- **Endpoint**: `POST /api/upload-api/upload`
- **Xác thực**: Bearer Token (Lấy trong phần Cài đặt giao diện Web).
- **Tham số**: `file` (multipart/form-data), `path` (tùy chọn), `share` (tùy chọn, đặt "public" để lấy link chia sẻ ngay).

Bạn có thể xem tài liệu chi tiết và ví dụ lệnh `curl` trực tiếp trong phần **Cài đặt -> Upload API** trên giao diện web.

---

## 🐳 Hướng dẫn cài đặt bằng Docker

Đây là cách triển khai được khuyến nghị cho máy chủ (server), giúp dễ dàng quản lý, cập nhật và không cần lo về môi trường hệ điều hành.

### 🎬 Tích hợp sẵn FFmpeg và yt-dlp

Image Docker sử dụng nền tảng Alpine Linux và **đã tích hợp sẵn FFmpeg và yt-dlp**.
Bạn **không cần** cài đặt hay mount các file thực thi bên ngoài. Tính năng tạo ảnh thu nhỏ và tải phương tiện từ URL hoạt động ngay lập tức!

---

### Phương pháp 1: Chạy bằng lệnh Docker (Nhanh nhất)

Cách nhanh nhất để chạy TeleCloud — chỉ cần Docker, không cần Compose.

#### Yêu cầu
- Đã cài đặt [Docker](https://docs.docker.com/engine/install/)

#### Các bước thực hiện

1. Tải image về:
```bash
docker pull ghcr.io/dabeecao/telecloud-go
```

2. Cấu hình `.env`:
```bash
mkdir telecloud && cd telecloud
curl -O https://raw.githubusercontent.com/dabeecao/telecloud-go/main/env.example
mv env.example .env
# Mở .env và điền API_ID, API_HASH, LOG_GROUP_ID
```

3. Xác thực tài khoản (chỉ thực hiện lần đầu):
```bash
mkdir -p data
sudo chmod 777 data
sudo docker run --rm -it \
    -v "$(pwd)/data:/app/data" \
    --env-file .env \
    -e DATABASE_PATH=/app/data/database.db \
    -e SESSION_FILE=/app/data/session.json \
    --user 65532:65532 \
    ghcr.io/dabeecao/telecloud-go -auth
```

4. Khởi động:
```bash
sudo docker run -d \
    --name telecloud \
    --restart unless-stopped \
    -p 8091:8091 \
    -v "$(pwd)/data:/app/data" \
    --env-file .env \
    -e DATABASE_PATH=/app/data/database.db \
    -e THUMBS_DIR=/app/data/thumbs \
    -e TEMP_DIR=/app/data/temp \
    -e SESSION_FILE=/app/data/session.json \
    --user 65532:65532 \
    ghcr.io/dabeecao/telecloud-go
```

Truy cập giao diện web tại: `http://localhost:8091`

**Lần đầu tiên truy cập**, hệ thống sẽ yêu cầu bạn tạo tài khoản và mật khẩu quản trị (Admin).

> 📁 Toàn bộ dữ liệu được lưu trong thư mục `./data/` trên máy chủ của bạn.

---

### Phương pháp 2: Docker Compose (Khuyên dùng)

#### Yêu cầu
- [Docker](https://docs.docker.com/engine/install/) và [Docker Compose](https://docs.docker.com/compose/install/) đã được cài đặt

#### 1. Tải về file cấu hình

Bạn chỉ cần tải file `docker-compose.yml` và `.env` mẫu:

```bash
mkdir telecloud && cd telecloud
curl -O https://raw.githubusercontent.com/dabeecao/telecloud-go/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/dabeecao/telecloud-go/main/env.example
mv env.example .env
```

*(Hoặc clone cả project nếu bạn muốn: `git clone https://github.com/dabeecao/telecloud-go.git`)*

#### 2. Cấu hình môi trường

Mở `.env` và điền các thông tin bắt buộc:

```env
API_ID=your_api_id
API_HASH=your_api_hash
LOG_GROUP_ID=me
PORT=8091
```

> Các biến `DATABASE_PATH`, `THUMBS_DIR`, `TEMP_DIR` được docker-compose tự động ghi đè để lưu vào thư mục `./data/` — bạn **không cần** thay đổi chúng trong `.env` khi dùng Docker.

#### 3. Xác thực tài khoản Telegram (Chỉ thực hiện lần đầu)

```bash
sudo docker compose run --rm -it telecloud -auth
```

Nhập số điện thoại, mã OTP và mật khẩu 2FA (nếu có). Sau khi thành công, file `session.json` sẽ được lưu vào `./data/`.

#### 4. Khởi động

```bash
sudo docker compose up -d
```

Truy cập giao diện web tại: `http://localhost:8091`

**Lần đầu tiên truy cập**, hệ thống sẽ yêu cầu bạn tạo tài khoản và mật khẩu quản trị (Admin).

#### Các lệnh hữu ích

```bash
# Xem log
sudo docker compose logs -f

# Dừng ứng dụng
sudo docker compose stop

# Cập nhật lên phiên bản mới
sudo docker compose pull
sudo docker compose up -d

# Xóa container (dữ liệu trong ./data/ vẫn được giữ nguyên)
sudo docker compose down
```

> 📁 Toàn bộ dữ liệu (database, ảnh thumbnail, file tạm) được lưu trong thư mục `./data/` trên máy chủ của bạn.


---

## 🛠️ Build từ nguồn (Dành cho nhà phát triển)

Nếu bạn muốn tự biên dịch dự án, có hai phương pháp:

### Phương pháp 1: Build bằng Docker (Khuyên dùng)

Cách đơn giản nhất để build từ nguồn — không cần cài Go, Node.js hay Tailwind CLI trên máy. Docker xử lý toàn bộ quá trình build.

#### Yêu cầu
- Đã cài đặt [Docker](https://docs.docker.com/engine/install/)

#### Các bước thực hiện

1. Clone dự án:
```bash
git clone --recursive https://github.com/dabeecao/telecloud-go.git
cd telecloud-go
```
*Nếu đã clone theo cách thông thường, chạy: `git submodule update --init --recursive`*

2. Build Docker image từ nguồn:
```bash
sudo docker build -t telecloud:local .
```

3. Cấu hình `.env`:
```bash
cp env.example .env
# Mở .env và điền API_ID, API_HASH, LOG_GROUP_ID
```

4. Xác thực tài khoản (chỉ thực hiện lần đầu):
```bash
mkdir -p data
sudo chmod 777 data
sudo docker run --rm -it \
    -v "$(pwd)/data:/app/data" \
    --env-file .env \
    -e DATABASE_PATH=/app/data/database.db \
    -e SESSION_FILE=/app/data/session.json \
    --user 65532:65532 \
    telecloud:local -auth
```

5. Chạy image vừa build:
```bash
sudo docker run -d \
    --name telecloud \
    --restart unless-stopped \
    -p 8091:8091 \
    -v "$(pwd)/data:/app/data" \
    --env-file .env \
    -e DATABASE_PATH=/app/data/database.db \
    -e THUMBS_DIR=/app/data/thumbs \
    -e TEMP_DIR=/app/data/temp \
    -e SESSION_FILE=/app/data/session.json \
    --user 65532:65532 \
    telecloud:local
```

Truy cập giao diện web tại: `http://localhost:8091`

> Bạn cũng có thể dùng file `docker-compose.yml` — chỉ cần thay dòng `image:` thành `image: telecloud:local` (hoặc thêm `build: .`) thay vì kéo image từ registry.

---

### Phương pháp 2: Build thủ công (Native)

1.  Cài đặt **Golang (1.24+)** tại https://golang.org/dl/

2.  Clone dự án (Bắt buộc dùng `--recursive` để lấy code frontend):
    ```bash
    git clone --recursive https://github.com/dabeecao/telecloud-go.git
    ```
    *Nếu bạn đã lỡ clone theo cách thông thường, hãy chạy lệnh sau để lấy code frontend:*
    `git submodule update --init --recursive`

3.  Cấu hình `.env` như hướng dẫn trên.

4. Chạy lệnh `go mod tidy` để tải về các thư viện cần thiết.

5. Build giao diện (Tailwind CSS, tải thư viện và Minify JS/CSS):
   * Yêu cầu: Đã cài đặt **Node.js** và **npm** trên máy để thực hiện minify (sử dụng `esbuild` qua `npx`).
   * Tải **Tailwind CLI** phù hợp với hệ điều hành của bạn tại [Tailwind CSS Releases](https://github.com/tailwindlabs/tailwindcss/releases/latest).
   * Đổi tên file vừa tải thành `tailwindcss` (hoặc `tailwindcss.exe` on Windows) và đặt vào thư mục **`web/`** của dự án.
   * **Lưu ý quan trọng**: Do các file đã nén (`.min.js`, `.min.css`) không được lưu trên GitHub để giữ repo sạch sẽ, bạn **bắt buộc** phải chạy lệnh build này trước khi build dự án Go, nếu không lệnh `go build` sẽ báo lỗi thiếu file.
   * Chạy lệnh build (script này nằm trong thư mục `web/`):
     ```bash
     # Linux/macOS
     cd web
     chmod +x build-frontend.sh
     ./build-frontend.sh
     cd ..

     # Windows
     cd web
     build-frontend.bat
     cd ..
     ```

6.  Chạy trực tiếp: `go run .`

7.  Hoặc build binary: `go build -o telecloud`

---

## ⚠️ Điều khoản sử dụng & Miễn trừ trách nhiệm

Dự án **TeleCloud** được phát triển nhằm mục đích lưu trữ và quản lý tệp tin cá nhân hợp pháp. Chúng tôi không chịu trách nhiệm đối với bất kỳ nội dung nào được người dùng tải lên hoặc các vi phạm điều khoản sử dụng của Telegram. Người dùng **hoàn toàn tự chịu trách nhiệm** cho hành vi sử dụng của mình.

Dự án được cung cấp **"nguyên trạng" (as-is)**, không có bất kỳ đảm bảo nào về tính ổn định hay bảo mật.

---

## 🌍 Đóng góp bản dịch (Localization)

Nếu bạn muốn đóng góp bản dịch cho một ngôn ngữ mới hoặc cải thiện bản dịch hiện có, hãy làm theo các bước sau:

> [!IMPORTANT]
> Toàn bộ mã nguồn giao diện (frontend) của TeleCloud nằm ở repository riêng: [**dabeecao/telecloud-frontend**](https://github.com/dabeecao/telecloud-frontend). Mọi đóng góp liên quan đến giao diện và bản dịch nên được thực hiện thông qua Pull Request tại repository này.

1.  **Tìm tệp bản dịch**: Các tệp ngôn ngữ nằm trong thư mục `static/locales/` (trong repository frontend) dưới định dạng JSON (ví dụ: `vi.json`, `en.json`).
2.  **Thêm ngôn ngữ mới**:
    *   Tạo một tệp JSON mới với mã ngôn ngữ ISO (ví dụ: `fr.json` cho tiếng Pháp).
    *   Sao chép nội dung từ `en.json` và dịch các giá trị sang ngôn ngữ của bạn.
    *   Mở tệp `static/js/common.js` và thêm ngôn ngữ mới vào mảng `availableLangs`:
        ```javascript
        { code: 'fr', name: 'Français', flag: '🇫🇷' }
        ```
3.  **Gửi Pull Request**: Gửi PR vào repository [telecloud-frontend](https://github.com/dabeecao/telecloud-frontend). Sau khi bản dịch được chấp nhận, nó sẽ được cập nhật vào dự án chính thông qua submodule.

---

## 🙏 Đóng góp

Dự án sử dụng các thư viện tuyệt vời: 
* [gotd/td](https://github.com/gotd/td): Telegram client, in Go. (MTProto API)
* [Gin](https://github.com/gin-gonic/gin): Gin is a high-performance HTTP web framework written in Go. It provides a Martini-like API but with significantly better performance—up to 40 times faster—thanks to httprouter. Gin is designed for building REST APIs, web applications, and microservices.
* [AlpineJS](https://github.com/alpinejs/alpine): A rugged, minimal framework for composing JavaScript behavior in your markup.
* [TailwindCSS](https://github.com/tailwindlabs/tailwindcss): A utility-first CSS framework for rapid UI development.
* [plyr](https://github.com/sampotts/plyr): A simple HTML5, YouTube and Vimeo player
* [Prism.js](https://github.com/PrismJS/prism): Lightweight, extensible syntax highlighter — dùng để tô màu code trong tính năng xem trước tệp.
* [FontAwesome](https://fontawesome.com): Bộ biểu tượng phổ biến nhất thế giới.
* [yt-dlp](https://github.com/yt-dlp/yt-dlp): A feature-rich command-line audio/video downloader.
* [Google Fonts (Nunito)](https://fonts.google.com/specimen/Nunito): Một bộ font chữ sans-serif hiện đại và dễ đọc.

Xin cảm ơn các đội ngũ phát triển đã cung cấp những công cụ hữu ích cho cộng đồng.

**Một phần mã nguồn của dự án và readme này được tham khảo và chỉnh sửa bởi Gemini AI**

---

## 📜 Giấy phép

Dự án này được phát hành dưới giấy phép [GNU Affero General Public License v3.0 (AGPL-3.0)](https://www.gnu.org/licenses/agpl-3.0.html).

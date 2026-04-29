# TeleCloud

<div align="center">

🇻🇳 Tiếng Việt | [🇺🇸 English](./readme_en.md)

</div>

**TeleCloud** là một dự án cho phép sử dụng chính dung lượng lưu trữ gần như vô tận của Telegram để lưu trữ và quản lý tệp.

Dự án này đã được **viết lại hoàn toàn bằng Golang** từ dự án gốc [dabeecao/tele-cloud](https://github.com/dabeecao/tele-cloud) , đem lại hiệu năng xuất sắc, sử dụng bộ nhớ cực thấp và khả năng biên dịch thành một file thực thi (binary) duy nhất có thể chạy ở bất kỳ đâu mà không cần cài đặt môi trường phát triển.

---

## 📸 Ảnh xem trước giao diện

### 🖥️ Giao diện Máy tính
| | | |
| :---: | :---: | :---: |
| <img src="preview/preview.jpg" width="100%"> | <img src="preview/preview-2.jpg" width="100%"> | <img src="preview/preview-3.jpg" width="100%"> |

### 📱 Giao diện Điện thoại
| | | | |
| :---: | :---: | :---: | :---: |
| <img src="preview/preview-4.jpg" width="100%"> | <img src="preview/preview-5.jpg" width="100%"> | <img src="preview/preview-6.jpg" width="100%"> | <img src="preview/preview-7.jpg" width="100%"> |

> *Giao diện được thiết kế tối ưu hóa cho mọi thiết bị (Responsive Design)*

## ✨ Tính năng

* 📁 Lưu trữ file trực tiếp trên Telegram không giới hạn dung lượng
* 🎬 Phát video và nhạc trực tiếp trong trang quản lý và liên kết chia sẻ
* 🔗 Liên kết chia sẻ có thể chọn liên kết thường hoặc link tải trực tiếp (Direct Link)
* 🗂️ Giao diện quản lý (File Browser) trực quan
* ⬆️ Upload song song (Multi-threading) tốc độ cao
* 📦 Upload chia nhỏ (chunk) để tối ưu tốc độ và ổn định
* 👤 Hỗ trợ **Userbot** với **MTProto** mạnh mẽ (tải lên file lớn đến 2GB/4GB)
* 📂 Hỗ trợ **WebDAV**: Gắn TeleCloud thành ổ đĩa mạng trên máy tính (Windows, macOS, Linux).
* 🔌 **Upload API**: Cho phép upload file từ xa qua HTTP API (Bearer Token) để tích hợp vào script hoặc CI/CD.
* 🌐 **Đa ngôn ngữ**: Hỗ trợ tiếng Việt và tiếng Anh ở giao diện sử dụng

---

## 🛠️ Cài đặt tự động (Linux / Termux / macOS / Raspberry Pi)

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

---

## 🚀 Hướng dẫn cài đặt nhanh (Sử dụng Binary đã biên dịch sẵn)

Đây là cách nhanh nhất để chạy TeleCloud mà không cần cài đặt môi trường lập trình.

### 1. Yêu cầu hệ thống
Bạn cần cài đặt **FFmpeg** để hệ thống có thể tạo ảnh thu nhỏ (thumbnail) cho video và tệp âm thanh.

*   **Ubuntu/Debian:** `sudo apt install ffmpeg`
*   **Redhat-base:** `sudo yum install ffmpeg` thông qua [Fedora and Red Hat Enterprise Linux packages
](https://rpmfusion.org/)
*   **Alpine Linux:** `apk add ffmpeg`
*   **Windows:** Tải bản build sẵn tại [ffmpeg.org](https://ffmpeg.org/download.html) và thêm vào PATH.

Nếu bạn không cài FFmpeg dự án vẫn có thể hoạt động nhưng tính năng tạo thumb (ảnh thu nhỏ của tệp) sẽ không hoạt động.

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
*   `MAX_UPLOAD_SIZE_MB`: Kích thước file tối đa được phép upload. Đặt là `0` để hệ thống tự động nhận diện (2GB cho tài khoản thường, 4GB cho Premium).
*   `DATABASE_PATH`: (Tùy chọn) Đường dẫn tới file database (mặc định: `database.db`).
*   `THUMBS_DIR`: (Tùy chọn) Đường dẫn tới thư mục chứa ảnh thumbnail (mặc định: `./static/thumbs`).
*   `TEMP_DIR`: (Tùy chọn) Đường dẫn thư mục tạm dùng để chứa các mảnh file (chunks) trong quá trình tải lên (mặc định: `./temp`).
*   `PROXY_URL`: (Tùy chọn) Proxy để kết nối MTProto, hỗ trợ HTTP và SOCKS5 (VD: `socks5://127.0.0.1:1080`).
*   `FFMPEG_PATH`: (Tùy chọn) Đường dẫn tới file FFmpeg (mặc định: `ffmpeg`). Đặt thành "disabled" để bỏ qua hình thu nhỏ video/âm thanh nếu FFmpeg không được cài đặt hoặc gây ra lỗi.
 
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

        # Hỗ trợ WebSockets (Cần thiết cho WebDAV và một số tính năng real-time)
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        # QUAN TRỌNG: Tắt buffering để hỗ trợ upload file lớn và streaming mượt hơn
        proxy_request_buffering off;
        proxy_buffering off;

        # Tăng timeout để tránh đứt kết nối khi xử lý file lớn
        proxy_read_timeout 3600s;
        proxy_connect_timeout 3600s;
        proxy_send_timeout 3600s;
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

### Yêu cầu
- [Docker](https://docs.docker.com/engine/install/) và [Docker Compose](https://docs.docker.com/compose/install/) đã được cài đặt

### 1. Tải về file cấu hình

Bạn chỉ cần tải file `docker-compose.yml` và `.env` mẫu:

```bash
mkdir telecloud && cd telecloud
curl -O https://raw.githubusercontent.com/dabeecao/telecloud-go/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/dabeecao/telecloud-go/main/env.example
mv env.example .env
```

*(Hoặc clone cả project nếu bạn muốn: `git clone https://github.com/dabeecao/telecloud-go.git`)*

### 2. Cấu hình môi trường

```bash
cp env.example .env
```

Mở `.env` và điền các thông tin bắt buộc:

```env
API_ID=your_api_id
API_HASH=your_api_hash
LOG_GROUP_ID=me
PORT=8091
```

> Các biến `DATABASE_PATH`, `THUMBS_DIR`, `TEMP_DIR` được docker-compose tự động ghi đè để lưu vào thư mục `./data/` — bạn **không cần** thay đổi chúng trong `.env` khi dùng Docker.

### 3. Xác thực tài khoản Telegram (Chỉ thực hiện lần đầu)

Lần đầu sử dụng, bạn cần đăng nhập tài khoản Telegram để tạo file phiên (`session.json`):

```bash
# Chạy xác thực tương tác (Docker sẽ tự động tải image về)
docker compose run --rm -it telecloud -auth
```

Nhập số điện thoại, mã OTP và mật khẩu 2FA (nếu có) theo hướng dẫn. Sau khi thành công, file `session.json` sẽ được lưu vào thư mục `./data/`.

### 4. Khởi động

```bash
docker compose up -d
```

Truy cập giao diện web tại: `http://localhost:8091`

**Lần đầu tiên truy cập**, hệ thống sẽ yêu cầu bạn tạo tài khoản và mật khẩu quản trị (Admin).

### Các lệnh hữu ích

```bash
# Xem log
docker compose logs -f

# Dừng ứng dụng
docker compose stop

# Cập nhật lên phiên bản mới
docker compose pull
docker compose up -d

# Xóa container (dữ liệu trong ./data/ vẫn được giữ nguyên)
docker compose down
```

> 📁 Toàn bộ dữ liệu (database, ảnh thumbnail, file tạm) được lưu trong thư mục `./data/` trên máy chủ của bạn.

### 🎬 (Tùy chọn) Bật FFmpeg để tạo thumbnail

Image Docker sử dụng nền tảng tối giản (`distroless`) nên **không đi kèm FFmpeg**. Nếu bạn muốn hỗ trợ tạo ảnh thu nhỏ (thumbnail) cho video và âm thanh, hãy cài FFmpeg trên máy chủ rồi mount binary vào container:

**Bước 1:** Cài FFmpeg trên máy chủ (nếu chưa có):
```bash
sudo apt install ffmpeg   # Ubuntu/Debian
```

**Bước 2:** Thêm vào `docker-compose.yml`:
```yaml
services:
  telecloud:
    volumes:
      - ./data:/app/data
      - /usr/bin/ffmpeg:/usr/bin/ffmpeg:ro   # Mount binary FFmpeg từ host
    environment:
      - FFMPEG_PATH=/usr/bin/ffmpeg           # Báo cho app biết đường dẫn
```

**Bước 3:** Khởi động lại container:
```bash
docker compose up -d
```

> 💡 Nếu không cần thumbnail, không cần làm gì thêm — ứng dụng vẫn hoạt động bình thường.

---

## 🛠️ Build từ nguồn (Dành cho nhà phát triển)

Nếu bạn muốn tự biên dịch dự án, hãy làm theo các bước sau:

1.  Cài đặt **Golang (1.21+)** tại https://golang.org/dl/

2.  Clone dự án: ```git clone https://github.com/dabeecao/telecloud-go.git```

3.  Cấu hình `.env` như hướng dẫn trên.

4. Chạy lệnh `go mod tidy` để tải về các thư viện cần thiết.

5. Build giao diện (Tailwind CSS và tải các thư viện):
   * Tải **Tailwind CLI** phù hợp với hệ điều hành của bạn tại [Tailwind CSS Releases](https://github.com/tailwindlabs/tailwindcss/releases/latest).
   * Đổi tên file vừa tải thành `tailwindcss` (hoặc `tailwindcss.exe` trên Windows) và đặt vào thư mục gốc của dự án.
   * Chạy lệnh build (script này sẽ tự động tải các thư viện như Alpine.js và Plyr):
     ```bash
     # Linux/macOS
     chmod +x build-frontend.sh
     ./build-frontend.sh

     # Windows
     build-frontend.bat
     ```

6.  Chạy trực tiếp: `go run .`

7.  Hoặc build binary: `go build -o telecloud`

---

## ⚠️ Điều khoản sử dụng & Miễn trừ trách nhiệm

Dự án **TeleCloud** được phát triển nhằm mục đích lưu trữ và quản lý tệp tin cá nhân hợp pháp. Chúng tôi không chịu trách nhiệm đối với bất kỳ nội dung nào được người dùng tải lên hoặc các vi phạm điều khoản sử dụng của Telegram. Người dùng **hoàn toàn tự chịu trách nhiệm** cho hành vi sử dụng của mình.

Dự án được cung cấp **"nguyên trạng" (as-is)**, không có bất kỳ đảm bảo nào về tính ổn định hay bảo mật.

---

## 🙏 Đóng góp

Dự án sử dụng các thư viện tuyệt vời: 
* [gotd/td](https://github.com/gotd/td): Telegram client, in Go. (MTProto API)
* [Gin](https://github.com/gin-gonic/gin): Gin is a high-performance HTTP web framework written in Go. It provides a Martini-like API but with significantly better performance—up to 40 times faster—thanks to httprouter. Gin is designed for building REST APIs, web applications, and microservices.
* [AlpineJS](https://github.com/alpinejs/alpine): A rugged, minimal framework for composing JavaScript behavior in your markup.
* [TailwindCSS](https://github.com/tailwindlabs/tailwindcss): A utility-first CSS framework for rapid UI development.
* [plyr](https://github.com/sampotts/plyr): A simple HTML5, YouTube and Vimeo player
* [FontAwesome](https://fontawesome.com): Bộ biểu tượng phổ biến nhất thế giới.
* [Google Fonts (Nunito)](https://fonts.google.com/specimen/Nunito): Một bộ font chữ sans-serif hiện đại và dễ đọc.

Xin cảm ơn các đội ngũ phát triển đã cung cấp những công cụ hữu ích cho cộng đồng.

**Một phần mã nguồn của dự án và readme này được tham khảo và chỉnh sửa bởi Gemini AI**

---

## 🎁 Ủng hộ

Nếu bạn thấy dự án hữu ích và muốn ủng hộ tôi cũng như các dự án sau của tôi, bạn có thể [truy cập vào đây](https://dabeecao.org#donate) và tặng tôi một tách trà.

---

## 📜 Giấy phép

Dự án này được phát hành dưới giấy phép [GNU Affero General Public License v3.0 (AGPL-3.0)](https://www.gnu.org/licenses/agpl-3.0.html).

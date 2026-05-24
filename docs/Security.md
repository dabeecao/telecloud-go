# 🔐 Security Policy & Hardening / Chính sách bảo mật & Hardening

Detailed information about security architecture, hardening measures, recommendations, and known limitations in TeleCloud.
Thông tin chi tiết về kiến trúc bảo mật, các biện pháp hardening, khuyến nghị vận hành và hạn chế đã biết của TeleCloud.

---

## 🇻🇳 Tiếng Việt

### 1. Các biện pháp bảo mật mặc định (Hardening)

TeleCloud tích hợp sẵn các tiêu chuẩn bảo mật tối ưu nhằm bảo vệ tối đa dữ liệu của bạn:

*   **Mã hóa AES-256-GCM**: Toàn bộ session Telegram và các cấu hình nhạy cảm (`api_id`, `api_hash`, `log_group_id`, `bot_tokens`) đều được mã hóa bằng AES-256-GCM trước khi lưu vào Cơ sở dữ liệu. Tiến trình di chuyển tự động (migration) sẽ diễn ra khi hệ thống khởi động.
*   **Bảo mật hệ thống (systemd)**: Script `auto-setup.sh` tạo systemd service chạy dưới user riêng (`User=telecloud` hoặc `DynamicUser`), sử dụng các cờ hardening nghiêm ngặt như `NoNewPrivileges=true`, `ProtectSystem=strict`, `ProtectHome=true`, `PrivateTmp=true`. Đồng thời, tự động đối chiếu mã hash SHA-256 của tệp binary tải về với tệp `checksums.txt` chính thức của Release.
*   **Cơ chế ký HMAC cho tải trực tiếp**: Token tải trực tiếp (direct-download) được ký HMAC sử dụng khóa derived từ khóa chủ (master key) chứ không phụ thuộc vào mật khẩu admin. Vì vậy, việc đổi mật khẩu admin sẽ **không** làm mất hiệu lực các liên kết tải trực tiếp đã được phát hành trước đó.
*   **Bảo vệ tệp chia sẻ có mật khẩu**: Sử dụng token session ngẫu nhiên đã ký (signed session token) lưu trong cookie. Mật khẩu hash Bcrypt tuyệt đối **không** bao giờ được lưu trực tiếp hay gửi lại ở client.
*   **Bảo mật WebDAV**: Áp dụng giới hạn tần suất (rate limit) cơ bản trong bộ nhớ (5 lần thử / 15 phút cho mỗi địa chỉ IP) để giảm thiểu dò mật khẩu (brute-force). Bộ nhớ cache xác thực ngắn (2 phút), sử dụng chuỗi mã hóa SHA-256 của mật khẩu thay vì lưu mật khẩu thô. Lưu ý: bộ đếm sẽ bị reset khi khởi động lại server.
*   **Quản lý phiên đăng nhập (HTTP Session)**: Mỗi phiên làm việc đều có thuộc tính `expires_at` (mặc định 30 ngày). Khi người dùng thực hiện đổi mật khẩu, toàn bộ các phiên đăng nhập khác của tài khoản đó sẽ lập tức bị vô hiệu hóa. Một tác vụ ngầm tự động dọn dẹp các phiên hết hạn mỗi 6 giờ.
*   **Nhật ký hoạt động (Audit Log)**: Ghi lại đầy đủ các sự kiện quan trọng trong bảng `audit_log` (như đăng nhập, đăng xuất, đổi mật khẩu, admin reset mật khẩu tài khoản con, setup ban đầu, thay đổi cài đặt hệ thống). Nhật ký được lưu trữ mặc định trong 90 ngày.
*   **Ngăn chặn tấn công SSRF (SafeHTTPClient)**: Trình tải tệp từ xa (remote URL upload, yt-dlp) sử dụng client HTTP an sau (`SafeHTTPClient`) thực hiện phân giải DNS động và ghim (pin) địa chỉ IP tại thời điểm kết nối, chặn hoàn toàn các cuộc tấn công DNS Rebinding hướng tới dải IP riêng tư hoặc loopback.

### 2. Các hạn chế đã biết (Known Limitations)

*   **Telegram Cloud không mã hóa đầu cuối**: Máy chủ đám mây của Telegram **không** hỗ trợ mã hóa đầu cuối (E2EE) đối với các tệp tin lưu trữ. Vui lòng không lưu trữ các tài liệu cực kỳ nhạy cảm như giấy tờ tùy thân, ảnh riêng tư hoặc khóa bí mật dưới dạng thô. Nếu muốn, hãy chủ động mã hóa dữ liệu ở phía client (sử dụng `rclone crypt` hoặc Cryptomator) trước khi tải lên TeleCloud.
*   **Rủi ro tài khoản (Userbot)**: Telegram có chính sách cấm các tài khoản lạm dụng API/Userbot để làm kho lưu trữ tệp tin cá nhân trái quy định. Không có cơ chế khôi phục nếu tài khoản bị khóa. Khuyến nghị sử dụng tài khoản phụ (số điện thoại phụ) để vận hành TeleCloud nhằm tránh ảnh hưởng tài khoản chính.

---

## 🇺🇸 English

### 1. Out-of-the-Box Security Measures (Hardening)

TeleCloud includes robust, industry-standard security features to protect your instance by default:

*   **AES-256-GCM Encryption**: All Telegram sessions and sensitive configurations (`api_id`, `api_hash`, `log_group_id`, `bot_tokens`) are encrypted using AES-256-GCM at rest in the database.
*   **System Hardening (systemd)**: The automated `auto-setup.sh` script registers a systemd unit running under a dedicated user account (`User=telecloud` or `DynamicUser`) with strict security flags like `NoNewPrivileges=true`, `ProtectSystem=strict`, `ProtectHome=true`, and `PrivateTmp=true`. Additionally, the binary's SHA-256 hash is verified against the official `checksums.txt` in the release.
*   **HMAC-Signed Direct Download Links**: Direct download tokens are HMAC-signed using a key derived from the master key, not the admin password. Consequently, changing the admin password **does not** invalidate active shared links.
*   **Secure Password-Protected Shares**: Password protection uses signed, random session tokens stored in cookies. Bcrypt hashes are processed on the server and are never exposed to the client.
*   **WebDAV Authentication Hardening**: WebDAV enforces a basic in-memory rate limit of 5 failed attempts per 15 minutes per IP to mitigate brute-force attacks. The auth cache expires in 2 minutes, using a SHA-256 hash of the password rather than plaintext. Note: counters reset on server restart.
*   **Web Session Lifecycle Management**: Every HTTP session carries an `expires_at` column (defaulting to 30 days). Changing your password immediately invalidates all other active sessions of that user. A background cron job cleans up expired sessions every 6 hours.
*   **Audit Logging**: Critical events (login, logout, password changes, admin reset, setup completion, setting updates) are written to the `audit_log` table. Logs are retained for 90 days by default.
*   **SSRF Protection (SafeHTTPClient)**: Remote downloaders (remote URL upload, yt-dlp) run through `SafeHTTPClient` which re-resolves and pins target IPs at dial time, fully mitigating DNS Rebinding attacks targeting loopback or private IP ranges.

### 2. Known Limitations

*   **Telegram Storage Is Not End-to-End Encrypted**: Telegram's cloud storage does **not** encrypt your stored files end-to-end. Avoid uploading extremely sensitive raw files (IDs, private photos, credentials). We highly recommend using client-side encryption (`rclone crypt`, Cryptomator) before uploading files to TeleCloud.
*   **Account Termination Risk**: Telegram reserves the right to ban accounts utilizing API/Userbots for heavy personal cloud storage. Banned accounts cannot be recovered. We recommend running TeleCloud with a secondary phone number rather than your main Telegram account.

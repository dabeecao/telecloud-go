/**
 * TeleCloud Common Utilities & I18n
 */

const TeleCloud = {
    version: 'dev',
    lang: localStorage.getItem('lang') || (navigator.language.startsWith('vi') ? 'vi' : 'en'),

    translations: {
        vi: {
            home: 'Quản lý tệp',
            upload: 'Tải lên',
            new_folder: 'Thư mục mới',
            logout: 'Đăng xuất',
            login: 'Đăng nhập',
            refresh: 'Làm mới',
            search_placeholder: 'Tìm kiếm tập tin hoặc thư mục...',
            empty_folder: 'Thư mục này đang trống',
            no_results: 'Không tìm thấy kết quả phù hợp cho',
            clear_search: 'Xóa tìm kiếm',
            max_limit: 'Giới hạn tải lên',
            items_count: 'mục trong thư mục này',
            lang_vi: 'Tiếng Việt',
            lang_en: 'English',
            unlimited: 'Không giới hạn',
            online: 'Hệ thống trực tuyến',
            download: 'Tải xuống',
            rename: 'Đổi tên',
            move: 'Di chuyển',
            copy: 'Sao chép',
            delete: 'Xóa',
            share_link: 'Tạo Link Chia sẻ',
            revoke_link: 'Thu hồi Link',
            copy_share_link: 'Copy Link Chia sẻ',
            copy_direct_link: 'Copy Link tải Direct',
            link_share: 'Link chia sẻ',
            link_direct: 'Link trực tiếp',
            file_info: 'Thông tin tệp tin',
            size: 'Kích thước',
            date: 'Ngày tải lên',
            status: 'Trạng thái',
            public: 'Công khai',
            private: 'Riêng tư',
            cancel: 'Hủy',
            confirm: 'Xác nhận',
            close: 'Đóng',
            access_code: 'Nhập mật khẩu truy cập...',
            access_system: 'Truy cập hệ thống',
            connecting_tg: 'Đang kết nối tới Telegram...',
            wait_msg: 'Vui lòng không đóng trang này',
            success: 'Thành công!',
            failure: 'Thất bại!',
            preparing_upload: 'Đang chuẩn bị...',
            syncing_tg: 'Đang đồng bộ với Telegram...',
            done: 'Hoàn tất!',
            cancelled: 'Đã hủy',
            capacity: 'Dung lượng còn lại',
            upload_progress: 'Tiến trình tải lên',
            upload_done: 'Đã tải lên xong',
            clear_finished: 'Dọn dẹp các tệp đã tải xong',
            pushing: 'Đang đẩy lên',
            max_size: 'Kích thước tối đa mỗi tệp',
            drop_files: 'Thả file vào đây để tải lên',
            drop_modal: 'Kéo thả tệp vào đây',
            or_click: 'Hoặc click vào nút dưới đây để duyệt tệp',
            select_files: 'Chọn tệp từ thiết bị',
            name_placeholder: 'Tên Thư mục / Tập tin',
            actions: 'Thao tác',
            folder: 'Thư mục',
            pasting_items: 'Đang giữ {n} mục ({a})',
            paste_here: 'Dán vào đây',
            delete_confirm_title: 'Xóa dữ liệu',
            delete_confirm_msg: 'Bạn có chắc chắn muốn xóa mục này?',
            delete_batch_msg: 'Bạn có chắc chắn muốn xóa {n} mục đã chọn? Không thể hoàn tác!',
            rename_title: 'Đổi tên file/thư mục:',
            new_folder_title: 'Tạo thư mục mới',
            toast_dl_started: 'Luồng tải xuống đã bắt đầu!',
            toast_tg_timeout: 'Kết nối Telegram quá lâu, vui lòng thử lại sau.',
            toast_only_files: 'Vui lòng chỉ chọn các tệp tin (Chưa hỗ trợ tải cả thư mục).',
            toast_skipped_folders: 'Đã tự động bỏ qua các thư mục trong danh sách.',
            toast_preparing_dl: 'Đang chuẩn bị tải {n} tệp...',
            toast_pasted: 'Hoàn tất di chuyển/sao chép!',
            toast_deleted: 'Đã xóa {n} mục',
            toast_renamed: 'Đổi tên thành công',
            toast_revoked: 'Đã thu hồi link Public',
            toast_copied: 'Đã sao chép {t}!',
            toast_login_fail: 'Sai mật khẩu truy cập!',
            toast_created: 'Đã tạo: {n}',
            status_error: 'Lỗi',
            conn_error: 'Lỗi kết nối!',
            retry: 'Thử lại',
            waiting_slot: 'Chờ hàng đợi...',
            uploading_to_server: 'Đang đẩy lên máy chủ...',
            upload_cancelled_waiting: 'Đã hủy tải lên khi đang chờ hàng đợi',
            webdav_toggle_error: 'Không thể thay đổi trạng thái WebDAV!',
            setup_failed: 'Thiết lập thất bại',
            setup_error: 'Lỗi thiết lập',
            file_too_large_title: 'File quá lớn',
            file_too_large_msg: 'File "{f}" vượt quá giới hạn tải lên.',
            type_image: 'Hình ảnh',
            type_video: 'Video',
            type_audio: 'Âm thanh',
            type_code: 'Mã nguồn',
            type_archive: 'Tệp nén',
            type_doc: 'Tài liệu',
            type_unknown: 'Tập tin',
            // WebDAV Guide
            webdav_guide: 'WebDAV',
            webdav_desc: 'Sử dụng TeleCloud như một ổ đĩa mạng trên máy tính của bạn.',
            webdav_url: 'Địa chỉ máy chủ',
            webdav_user: 'Tên đăng nhập',
            webdav_note: 'Sử dụng tài khoản/mật khẩu quản trị để kết nối. Host: địa chỉ web, Giao thức: HTTP/HTTPS, Port: port của ứng dụng, Path: /webdav',
            webdav_setup_title: 'Hướng dẫn cấu hình',
            webdav_setup_guide: `<p class="mb-2">Cách điền phụ thuộc vào ứng dụng WebDAV bạn đang dùng:</p>
<div class="space-y-2 mb-2 pl-1.5 border-l-2 border-slate-200 dark:border-slate-700">
  <div><span class="font-bold text-slate-700 dark:text-slate-300">1. Có ô Port riêng (VD: RaiDrive)</span></div>
  <ul class="list-disc pl-5 space-y-1 text-slate-500 dark:text-slate-400">
    <li><span class="font-medium text-slate-700 dark:text-slate-300">Host:</span> IP/Tên miền của bạn (không kèm http hay port)</li>
    <li><span class="font-medium text-slate-700 dark:text-slate-300">Port:</span> Điền đúng số port web đang chạy</li>
    <li><span class="font-medium text-slate-700 dark:text-slate-300">Path:</span> /webdav</li>
    <li><span class="font-medium text-slate-700 dark:text-slate-300">Giao thức:</span> HTTP (hoặc HTTPS nếu có SSL)</li>
  </ul>
  <div class="mt-2.5"><span class="font-bold text-slate-700 dark:text-slate-300">2. Chỉ có 1 ô URL (VD: Windows Map Network Drive)</span></div>
  <ul class="list-disc pl-5 space-y-1 text-slate-500 dark:text-slate-400">
    <li>Bạn điền cả cụm: <span class="font-mono bg-slate-200/50 dark:bg-slate-700/50 px-1.5 py-0.5 rounded text-slate-700 dark:text-slate-300">http(s)://[IP]:[Port]/webdav</span></li>
  </ul>
</div>
<p class="text-[11px] italic opacity-80 mt-3 border-t border-slate-200 dark:border-slate-700 pt-2"><i class="fa-solid fa-lightbulb text-amber-500 mr-1"></i>Tóm lại: Nếu hỏi Port riêng, tách port ra. Nếu chỉ hỏi URL, gộp cả port vào URL.</p>`,
            settings: 'Cài đặt',
            settings_desc: 'Quản lý tài khoản và tùy chọn của bạn.',
            change_password: 'Đổi mật khẩu',
            old_password: 'Mật khẩu cũ',
            new_password: 'Mật khẩu mới',
            confirm_password: 'Xác nhận mật khẩu mới',
            save_changes: 'Lưu thay đổi',
            username: 'Tài khoản',
            password: 'Mật khẩu',
            setup_title: 'Chào mừng',
            setup_desc: 'Tạo tài khoản quản trị viên để tiếp tục',
            create_account: 'Tạo tài khoản',
            toast_pass_mismatch: 'Mật khẩu mới không khớp!',
            toast_pass_changed: 'Đổi mật khẩu thành công!',
            err_incorrect_old_password: 'Mật khẩu cũ không chính xác!',
            err_failed_to_hash_password: 'Lỗi mã hóa mật khẩu!',
            err_too_many_requests: 'Quá nhiều yêu cầu, vui lòng thử lại sau!',
            err_ip_blocked: 'Bạn đã bị chặn vì nhập sai mật khẩu quá nhiều lần!',
            err_db_error: 'Lỗi cơ sở dữ liệu!',
            err_unauthorized: 'Sai tài khoản hoặc mật khẩu!',
            err_failed_to_create_session: 'Không thể tạo phiên làm việc!',
            err_already_setup: 'Hệ thống đã được thiết lập trước đó!',
            err_failed_to_open_temp_file: 'Không thể mở tệp tạm thời trên máy chủ!',
            err_failed_to_seek_file: 'Lỗi định vị tệp tin!',
            err_failed_to_write_chunk: 'Lỗi ghi mảnh dữ liệu!',
            err_tg_msgid: 'Không lấy được ID tin nhắn từ Telegram!',
            setup_confirm_password: 'Xác nhận mật khẩu',
            // share.html specific
            preparing_file: 'Đang chuẩn bị file...',
            wait_tg: 'Kết nối máy chủ Telegram, vui lòng đợi',
            loading: 'Đang tải...',
            download_now: 'Tải xuống ngay',
            safe_msg: 'Đã quét an toàn bởi hệ thống',
            at_time: 'lúc',
            back_home: 'Quay lại trang chủ',
            access_failed: 'Truy cập thất bại',
            file_not_found_msg: 'Tệp không tồn tại hoặc link đã bị thu hồi.',
            update_title: 'Cập nhật TeleCloud',
            update_msg: 'Có phiên bản mới trên GitHub. Bạn có muốn xem không?',
            dont_remind_today: 'Không nhắc lại hôm nay',
            changelog: 'Cập nhật',
            latest_release: 'Bản mới nhất',
            update_now: 'Xem bản cập nhật',
            cloudflare_purge: 'Lưu ý: Nếu bạn dùng Cloudflare, hãy Purge Cache để cập nhật giao diện mới nhất.',
            // Upload API
            upload_api: 'Upload API',
            upload_api_desc: 'Cho phép upload file từ xa qua HTTP API với Bearer Token. Dùng cho tích hợp script, CI/CD, v.v.',
            upload_api_endpoint: 'Endpoint',
            upload_api_key_label: 'API Key (Bearer Token)',
            label_api_key: 'API Key',
            upload_api_no_key: 'Chưa có API Key. Nhấn Tạo mới để tạo.',
            upload_api_regenerate: 'Tạo key mới',
            upload_api_delete_key: 'Xóa key',
            api_key_regenerated: 'Đã tạo API Key mới!',
            api_key_deleted: 'Đã xóa API Key!',
            api_key_delete_title: 'Xóa API Key',
            api_key_delete_msg: 'Bạn có chắc muốn xóa API Key? Các tích hợp đang sử dụng key này sẽ bị mất quyền truy cập.',
            api_toggle_error: 'Không thể thay đổi trạng thái Upload API!',
            upload_api_doc_title: 'Tài liệu API',
            upload_api_show_key: 'Hiển thị key',
            upload_api_hide_key: 'Ẩn key',
            upload_api_copy_key: 'Sao chép key',
            upload_api_warning: 'Giữ API Key bí mật. Ai có key này có thể upload file vào TeleCloud của bạn.',
            upload_api_params: 'Tham số (multipart/form-data)',
            upload_api_param_file: 'Tệp tin cần upload',
            upload_api_param_path: 'Đường dẫn thư mục lưu trữ (mặc định: /)',
            upload_api_param_share: 'Đặt "public" để tự động tạo link chia sẻ và nhận share_link + direct_link trong kết quả',
            upload_api_response_title: 'Kết quả trả về (200 OK)',
            upload_api_sync_note: 'API đồng bộ — kết quả chỉ trả về khi upload Telegram <strong>xong hoàn toàn</strong>. Hãy chú ý <strong>Timeout</strong> của script/curl và <strong>giới hạn thời gian upload</strong> của Reverse Proxy nếu có.',
            curl_basic: 'cURL — Cơ bản',
            curl_share: 'cURL — Tự động tạo link chia sẻ',
            curl_async: 'cURL — Bất đồng bộ (Background)',
            response_share: 'Kết quả trả về — Với share=public',
            response_async: 'Kết quả trả về — Với async=true',
            upload_api_param_async: 'Đặt "true" để upload chạy ngầm và nhận task_id ngay lập tức (Không áp dụng nếu share=public)'
        },
        en: {
            home: 'File Manager',
            upload: 'Upload',
            new_folder: 'New Folder',
            logout: 'Logout',
            login: 'Login',
            refresh: 'Refresh',
            search_placeholder: 'Search files or folders...',
            empty_folder: 'This folder is empty',
            no_results: 'No matching results for',
            clear_search: 'Clear search',
            max_limit: 'Upload limit',
            items_count: 'items in this folder',
            lang_vi: 'Tiếng Việt',
            lang_en: 'English',
            unlimited: 'Unlimited',
            online: 'System Online',
            download: 'Download',
            rename: 'Rename',
            move: 'Move',
            copy: 'Copy',
            delete: 'Delete',
            share_link: 'Create Share Link',
            revoke_link: 'Revoke Link',
            copy_share_link: 'Copy Share Link',
            copy_direct_link: 'Copy Direct Link',
            link_share: 'Share link',
            link_direct: 'Direct link',
            file_info: 'File Info',
            size: 'Size',
            capacity: 'Remaining Capacity',
            date: 'Upload Date',
            status: 'Status',
            public: 'Public',
            private: 'Private',
            cancel: 'Cancel',
            confirm: 'Confirm',
            close: 'Close',
            access_code: 'Enter access password...',
            access_system: 'Access System',
            connecting_tg: 'Connecting to Telegram...',
            wait_msg: 'Please do not close this page',
            success: 'Success!',
            failure: 'Failure!',
            preparing_upload: 'Preparing...',
            syncing_tg: 'Syncing with Telegram...',
            done: 'Done!',
            cancelled: 'Cancelled',
            upload_progress: 'Upload Progress',
            upload_done: 'Upload Finished',
            clear_finished: 'Clear finished uploads',
            pushing: 'Pushing',
            max_size: 'Max size per file',
            drop_files: 'Drop files here to upload',
            drop_modal: 'Drag & drop files here',
            or_click: 'Or click the button below to browse',
            select_files: 'Select files from device',
            name_placeholder: 'Name',
            actions: 'Actions',
            folder: 'Folder',
            pasting_items: 'Holding {n} items ({a})',
            paste_here: 'Paste here',
            delete_confirm_title: 'Delete Data',
            delete_confirm_msg: 'Are you sure you want to delete this item?',
            delete_batch_msg: 'Are you sure you want to delete {n} selected items? Cannot be undone!',
            rename_title: 'Rename file/folder:',
            new_folder_title: 'Create new folder',
            toast_dl_started: 'Download stream started!',
            toast_tg_timeout: 'Telegram connection took too long, please try again.',
            toast_only_files: 'Please select files only (Folder download not supported).',
            toast_skipped_folders: 'Automatically skipped folders in the list.',
            toast_preparing_dl: 'Preparing to download {n} files...',
            toast_pasted: 'Move/Copy complete!',
            toast_deleted: 'Deleted {n} items',
            toast_renamed: 'Rename successful',
            toast_revoked: 'Public link revoked',
            toast_copied: 'Copied {t}!',
            toast_login_fail: 'Wrong access password!',
            toast_created: 'Created: {n}',
            status_error: 'Error',
            conn_error: 'Connection Error!',
            retry: 'Retry',
            waiting_slot: 'Waiting in queue...',
            uploading_to_server: 'Uploading to server...',
            upload_cancelled_waiting: 'Upload cancelled while waiting',
            webdav_toggle_error: 'Failed to change WebDAV status!',
            setup_failed: 'Setup failed',
            setup_error: 'Setup error',
            file_too_large_title: 'File too large',
            file_too_large_msg: 'File "{f}" exceeds upload limit.',
            type_image: 'Image',
            type_video: 'Video',
            type_audio: 'Audio',
            type_code: 'Source Code',
            type_archive: 'Archive',
            type_doc: 'Document',
            type_unknown: 'File',
            // WebDAV Guide
            webdav_guide: 'WebDAV',
            webdav_desc: 'Use TeleCloud as a network drive on your computer.',
            webdav_url: 'Server Address',
            webdav_user: 'Username',
            webdav_note: 'Use the administrator account/password to connect. Host: web address, Protocol: HTTP/HTTPS, Port: application port, Path: /webdav',
            webdav_setup_title: 'Configuration Guide',
            webdav_setup_guide: `<p class="mb-2">Configuration depends on your WebDAV client:</p>
<div class="space-y-2 mb-2 pl-1.5 border-l-2 border-slate-200 dark:border-slate-700">
  <div><span class="font-bold text-slate-700 dark:text-slate-300">1. Separate Port field (e.g., RaiDrive)</span></div>
  <ul class="list-disc pl-5 space-y-1 text-slate-500 dark:text-slate-400">
    <li><span class="font-medium text-slate-700 dark:text-slate-300">Host:</span> Your IP/Domain (without http or port)</li>
    <li><span class="font-medium text-slate-700 dark:text-slate-300">Port:</span> The running web port</li>
    <li><span class="font-medium text-slate-700 dark:text-slate-300">Path:</span> /webdav</li>
    <li><span class="font-medium text-slate-700 dark:text-slate-300">Protocol:</span> HTTP (or HTTPS if SSL is enabled)</li>
  </ul>
  <div class="mt-2.5"><span class="font-bold text-slate-700 dark:text-slate-300">2. Single URL field (e.g., Windows Map Network Drive)</span></div>
  <ul class="list-disc pl-5 space-y-1 text-slate-500 dark:text-slate-400">
    <li>Enter the full URL: <span class="font-mono bg-slate-200/50 dark:bg-slate-700/50 px-1.5 py-0.5 rounded text-slate-700 dark:text-slate-300">http(s)://[IP]:[Port]/webdav</span></li>
  </ul>
</div>
<p class="text-[11px] italic opacity-80 mt-3 border-t border-slate-200 dark:border-slate-700 pt-2"><i class="fa-solid fa-lightbulb text-amber-500 mr-1"></i>Summary: If asked for a Port separately, extract it. Otherwise, include it in the URL.</p>`,
            settings: 'Settings',
            settings_desc: 'Manage your account and preferences.',
            change_password: 'Change Password',
            old_password: 'Old Password',
            new_password: 'New Password',
            confirm_password: 'Confirm New Password',
            save_changes: 'Save Changes',
            username: 'Username',
            password: 'Password',
            setup_title: 'Welcome',
            setup_desc: 'Create an admin account to continue',
            create_account: 'Create Account',
            toast_pass_mismatch: 'New passwords do not match!',
            toast_pass_changed: 'Password changed successfully!',
            err_incorrect_old_password: 'Incorrect old password!',
            err_failed_to_hash_password: 'Failed to hash password!',
            err_too_many_requests: 'Too many requests, please try again later!',
            err_ip_blocked: 'You have been blocked due to too many failed attempts!',
            err_db_error: 'Database error!',
            err_unauthorized: 'Invalid username or password!',
            err_failed_to_create_session: 'Failed to create session!',
            err_already_setup: 'System is already setup!',
            err_failed_to_open_temp_file: 'Failed to open temporary file on server!',
            err_failed_to_seek_file: 'File seeking error!',
            err_failed_to_write_chunk: 'Error writing data chunk!',
            err_tg_msgid: 'Failed to get MessageID from Telegram!',
            setup_confirm_password: 'Confirm Password',
            // share.html specific
            preparing_file: 'Preparing file...',
            wait_tg: 'Connecting to Telegram, please wait',
            loading: 'Loading...',
            download_now: 'Download Now',
            safe_msg: 'Safely scanned by system',
            at_time: 'at',
            back_home: 'Back to Home',
            access_failed: 'Access Failed',
            file_not_found_msg: 'File not found or link has been revoked.',
            update_title: 'TeleCloud Update',
            update_msg: 'A new version is available on GitHub. Do you want to check it out?',
            dont_remind_today: "Don't remind today",
            changelog: 'Changelog',
            latest_release: 'Latest Release',
            update_now: 'View Changelog',
            cloudflare_purge: 'Note: If you use Cloudflare, please Purge Cache to get the latest interface.',
            // Upload API
            upload_api: 'Upload API',
            upload_api_desc: 'Allow remote file uploads via HTTP API with Bearer Token. Use for scripts, CI/CD integrations, etc.',
            upload_api_endpoint: 'Endpoint',
            upload_api_key_label: 'API Key (Bearer Token)',
            label_api_key: 'API Key',
            upload_api_no_key: 'No API Key yet. Click Generate to create one.',
            upload_api_regenerate: 'Generate new key',
            upload_api_delete_key: 'Delete key',
            api_key_regenerated: 'New API Key generated!',
            api_key_deleted: 'API Key deleted!',
            api_key_delete_title: 'Delete API Key',
            api_key_delete_msg: 'Are you sure you want to delete the API Key? Integrations using this key will lose access.',
            api_toggle_error: 'Failed to change Upload API status!',
            upload_api_doc_title: 'API Documentation',
            upload_api_show_key: 'Show key',
            upload_api_hide_key: 'Hide key',
            upload_api_copy_key: 'Copy key',
            upload_api_warning: 'Keep your API Key secret. Anyone with this key can upload files to your TeleCloud.',
            upload_api_params: 'Parameters (multipart/form-data)',
            upload_api_param_file: 'File to upload',
            upload_api_param_path: 'Destination folder path (default: /)',
            upload_api_param_share: 'Set "public" to auto-create a share link and get share_link + direct_link in response',
            upload_api_response_title: 'Response (200 OK)',
            upload_api_sync_note: 'Synchronous API — the response is only returned when the Telegram upload is <strong>fully complete</strong>. Pay attention to script/curl <strong>Timeout</strong> and Reverse Proxy <strong>upload time limit</strong> if applicable.',
            curl_basic: 'cURL — Basic',
            curl_share: 'cURL — Auto Share Link',
            curl_async: 'cURL — Asynchronous (Background)',
            response_share: 'Response — With share=public',
            response_async: 'Response — With async=true',
            upload_api_param_async: 'Set "true" to upload in background and get task_id immediately (Ignored if share=public)'
        }
    },

    t(key, params = {}, lang = this.lang) {
        let text = (this.translations[lang] && this.translations[lang][key]) || key;
        Object.keys(params).forEach(p => {
            text = text.replace(`{${p}}`, params[p]);
        });
        return text;
    },

    toggleLang() {
        this.lang = this.lang === 'vi' ? 'en' : 'vi';
        localStorage.setItem('lang', this.lang);
        return this.lang;
    },

    setLang(l) {
        this.lang = l;
        localStorage.setItem('lang', l);
        return l;
    },

    formatBytes(bytes, decimals = 2) {
        if (!+bytes) return '0 B';
        const k = 1024;
        const dm = decimals < 0 ? 0 : decimals;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
    },

    formatDate(dateStr, lang = this.lang) {
        if (!dateStr) return '--';
        let d;
        if (dateStr.includes('T')) {
            d = new Date(dateStr);
        } else {
            const safeString = dateStr.replace(' ', 'T') + 'Z';
            d = new Date(safeString);
        }
        if (isNaN(d.getTime())) return dateStr;
        const options = { hour: '2-digit', minute: '2-digit' };
        if (lang === 'vi') {
            return d.toLocaleDateString('vi-VN') + ' ' + this.t('at_time', {}, lang) + ' ' + d.toLocaleTimeString('vi-VN', options);
        }
        return d.toLocaleDateString('en-US') + ' ' + this.t('at_time', {}, lang) + ' ' + d.toLocaleTimeString('en-US', options);
    },

    parseMarkdown(text) {
        if (!text) return '';
        return text
            .replace(/^### (.*$)/gim, '<h3 class="text-base font-bold mt-3 mb-1">$1</h3>')
            .replace(/^## (.*$)/gim, '<h2 class="text-lg font-bold mt-4 mb-2">$1</h2>')
            .replace(/^# (.*$)/gim, '<h1 class="text-xl font-bold mt-5 mb-2">$1</h1>')
            .replace(/^\* (.*$)/gim, '<li class="ml-4 list-disc">$1</li>')
            .replace(/^\- (.*$)/gim, '<li class="ml-4 list-disc">$1</li>')
            .replace(/\*\*(.*)\*\*/gim, '<strong>$1</strong>')
            .replace(/\*(.*)\*/gim, '<em>$1</em>')
            .replace(/`(.*?)`/gim, '<code class="bg-slate-200 dark:bg-slate-800 px-1 rounded font-mono text-xs">$1</code>')
            .replace(/\n/gim, '<br>');
    },

    getFileTypeData(filename) {
        if (!filename) return { n: this.t('type_unknown'), c: 'bg-slate-100 dark:bg-slate-800', i: '<i class="fa-solid fa-file text-2xl"></i>' };
        const ext = filename.split('.').pop().toLowerCase();
        const types = {
            'jpg': { n: this.t('type_image'), c: 'bg-rose-100 text-rose-500 dark:bg-rose-500/20 dark:text-rose-400', i: '<i class="fa-solid fa-image text-2xl"></i>' },
            'jpeg': 'jpg', 'png': 'jpg', 'gif': 'jpg', 'webp': 'jpg', 'svg': 'jpg',
            'mp4': { n: this.t('type_video'), c: 'bg-purple-100 text-purple-500 dark:bg-purple-500/20 dark:text-purple-400', i: '<i class="fa-solid fa-film text-2xl"></i>' },
            'mov': 'mp4', 'avi': 'mp4', 'mkv': 'mp4',
            'mp3': { n: this.t('type_audio'), c: 'bg-amber-100 text-amber-500 dark:bg-amber-500/20 dark:text-amber-400', i: '<i class="fa-solid fa-music text-2xl"></i>' },
            'wav': 'mp3', 'flac': 'mp3',
            'php': { n: this.t('type_code'), c: 'bg-indigo-100 text-indigo-500 dark:bg-indigo-500/20 dark:text-indigo-400', i: '<i class="fa-solid fa-code text-2xl"></i>' },
            'js': 'php', 'html': 'php', 'css': 'php', 'py': 'php', 'json': 'php', 'sql': 'php',
            'zip': { n: this.t('type_archive'), c: 'bg-orange-100 text-orange-500 dark:bg-orange-500/20 dark:text-orange-400', i: '<i class="fa-solid fa-file-zipper text-2xl"></i>' },
            'rar': 'zip', 'ipa': 'zip', 'tar': 'zip', 'gz': 'zip', '7z': 'zip', 'apk': 'zip',
            'pdf': { n: this.t('type_doc'), c: 'bg-red-100 text-red-500 dark:bg-red-500/20 dark:text-red-400', i: '<i class="fa-solid fa-file-pdf text-2xl"></i>' },
            'doc': 'pdf', 'docx': 'pdf', 'xls': 'pdf', 'xlsx': 'pdf', 'txt': 'pdf'
        };
        let result = types[ext];
        if (typeof result === 'string') result = types[result];
        return result || { n: this.t('type_unknown') + ' (' + ext.toUpperCase() + ')', c: 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400', i: '<i class="fa-solid fa-file text-2xl"></i>' };
    },

    /**
     * Reads the CSRF token from the csrf_token cookie set by the server.
     * Use this to attach an X-CSRF-Token header to all POST/PUT/DELETE fetch requests.
     * @returns {string} The CSRF token, or empty string if not found.
     */
    getCsrfToken() {
        const match = document.cookie.match(/(?:^|;\s*)csrf_token=([^;]+)/);
        return match ? decodeURIComponent(match[1]) : '';
    },

    /**
     * Copies text to the clipboard with fallback for non-secure contexts (HTTP).
     * @param {string} text The text to copy.
     * @returns {Promise} A promise that resolves when the text is copied.
     */
    async copyToClipboard(text) {
        if (navigator.clipboard && window.isSecureContext) {
            try {
                await navigator.clipboard.writeText(text);
                return true;
            } catch (err) {
                console.error('navigator.clipboard.writeText failed', err);
            }
        }

        // Fallback for non-secure contexts (HTTP) or failure
        const textArea = document.createElement("textarea");
        textArea.value = text;
        textArea.style.position = "fixed";
        textArea.style.left = "-9999px";
        textArea.style.top = "0";
        document.body.appendChild(textArea);
        textArea.focus();
        textArea.select();
        let success = false;
        try {
            success = document.execCommand('copy');
        } catch (err) {
            console.error('Fallback copy failed', err);
        }
        document.body.removeChild(textArea);
        if (!success) throw new Error('Copy failed');
        return true;
    }
};
